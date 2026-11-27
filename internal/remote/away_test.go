package remote

import (
	"strings"
	"testing"
)

// TestAwayRoutes: POST /api/view passes a phone's beats and its leaving
// on, POST /api/away the dismiss; both want a paired device.
func TestAwayRoutes(t *testing.T) {
	s, _ := newServer(t)
	var got []string
	s.Tabs = func() []Tab { return nil }
	s.View = func(pane string, on bool) { got = append(got, pane+map[bool]string{true: " on", false: " off"}[on]) }
	s.AwayDismiss = func(pane string) { got = append(got, pane+" dismiss") }
	h := s.Handler()
	token := pairDevice(t, s, h, "pixel")
	for _, body := range []string{`{"pane":"p1","on":true}`, `{"pane":"p1","on":false}`} {
		if st, b := call(t, h, "POST", "/api/view", token, body); st != 200 {
			t.Errorf("view %s: %d %s", body, st, b)
		}
	}
	if st, _ := call(t, h, "POST", "/api/away", token, `{"pane":"p1"}`); st != 200 {
		t.Errorf("away: %d", st)
	}
	if st, _ := call(t, h, "POST", "/api/view", "", `{"pane":"p1","on":true}`); st != 401 {
		t.Errorf("view without a token: %d", st)
	}
	if strings.Join(got, ",") != "p1 on,p1 off,p1 dismiss" {
		t.Errorf("calls %q", got)
	}
}
