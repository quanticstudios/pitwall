package sidebar

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestDevURLClicks(t *testing.T) {
	w := model.Workspace{ID: "w", Branch: "fix/login", WorktreeRoot: "/srv/web-app", Ports: model.PortBlock{First: 3010, Last: 3019}}
	s := Sidebar{DevProxyPort: 7778}
	v := &view{st: &model.State{Listening: map[string][]int{"w": {3010}}}}
	s.rows = map[string]*rowState{}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Constraints: layout.Exact(image.Pt(288, 800))}
	for _, port := range []int{7778, 0} {
		s.DevProxyPort = port
		s.events = nil
		s.row(w.ID).port.Click()
		s.prClicks(gtx, v, w)
		if len(s.events) != 1 || s.events[0] != (OpenPort{Port: 3010, URL: w.DevURL(port)}) {
			t.Fatalf("port %d: %v", port, s.events)
		}
	}
	s.events = nil
	s.hover.shown, s.cardURL = w.ID, w.DevURL(7778)
	s.cardPort.Click()
	s.radarInput(gtx)
	if len(s.events) != 1 || s.events[0] != (OpenPort{URL: s.cardURL}) {
		t.Fatalf("hover events: %v", s.events)
	}
}

func TestDevURLLocalOnly(t *testing.T) {
	w := model.Workspace{ID: "w", Branch: "fix/login", WorktreeRoot: "/srv/web-app", Ports: model.PortBlock{First: 3010, Last: 3019}}
	s := Sidebar{DevProxyPort: 7778}
	if s.devURL(w) != "http://fix-login.web-app.localhost:7778" {
		t.Fatal("local URL missing")
	}
	s.DevProxyPort = 0
	if s.devURL(w) != "" {
		t.Fatal("disabled proxy URL")
	}
	s.DevProxyPort, s.Host = 7778, "box"
	if s.devURL(w) != "" {
		t.Fatal("remote URL")
	}
	s.Host = ""
	s.HostOf = func(string) string { return "box" }
	if s.devURL(w) != "" {
		t.Fatal("shared remote URL")
	}
}
