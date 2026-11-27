package remote

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// The Screen view's routes: the screen redacted, only the key bar's keys,
// refusals passed on, and a log line per key and stop.
func TestScreenKeyStop(t *testing.T) {
	s, _ := newServer(t)
	var keys, stops []string
	s.Tabs = func() []Tab { return nil }
	s.Screen = func(pane string) (string, error) {
		if pane != "p1" {
			return "", errors.New("no pane " + pane)
		}
		return "> token=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789\n❯ 1. Yes", nil
	}
	s.Key = func(pane, key string) error {
		if pane == "guarded" {
			return errors.New("the tab is guarded")
		}
		keys = append(keys, pane+" "+key)
		return nil
	}
	s.Stop = func(pane string) error { stops = append(stops, pane); return nil }
	h := s.Handler()
	token := pairDevice(t, s, h, "pixel")
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(old) })

	if st, body := call(t, h, "GET", "/api/screen?pane=p1", token, ""); st != 200 || strings.Contains(body, "abcdefghijklmnop") || !strings.Contains(body, "1. Yes") {
		t.Errorf("screen: %d %s", st, body)
	}
	if st, _ := call(t, h, "GET", "/api/screen?pane=gone", token, ""); st != 409 {
		t.Errorf("screen of an unknown pane: %d", st)
	}
	if st, _ := call(t, h, "GET", "/api/screen?pane=p1", "", ""); st != 401 {
		t.Errorf("screen without a token: %d", st)
	}
	for _, k := range []string{"esc", "shift+tab", "ctrl+c", "9"} {
		if st, body := call(t, h, "POST", "/api/key", token, `{"pane":"p1","key":"`+k+`"}`); st != 200 {
			t.Errorf("key %s: %d %s", k, st, body)
		}
	}
	for _, k := range []string{"x", "ctrl+d", "0", ""} {
		if st, _ := call(t, h, "POST", "/api/key", token, `{"pane":"p1","key":"`+k+`"}`); st != 400 {
			t.Errorf("key %q off the bar: %d", k, st)
		}
	}
	if st, body := call(t, h, "POST", "/api/key", token, `{"pane":"guarded","key":"enter"}`); st != 409 || !strings.Contains(body, "guarded") {
		t.Errorf("guarded key: %d %s", st, body)
	}
	if strings.Join(keys, ",") != "p1 esc,p1 shift+tab,p1 ctrl+c,p1 9" {
		t.Errorf("keys %q", keys)
	}
	if st, _ := call(t, h, "POST", "/api/stop", token, `{"pane":"p1"}`); st != 200 || len(stops) != 1 {
		t.Errorf("stop: %d %v", st, stops)
	}
	logged := out.String()
	for _, want := range []string{`remote: "pixel": key esc sent to pane p1`, `remote: "pixel": key enter to pane guarded refused`, `remote: "pixel": stop sent to pane p1`} {
		if !strings.Contains(logged, want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
}
