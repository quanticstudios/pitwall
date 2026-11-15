package daemon

// The daemon's side of the control commands on the JSON socket
// (internal/web/control.go): copies of the state, pane text, and
// notices of changes and pane exits.

import (
	"errors"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/web"
)

// ctlHub is the daemon as web.Control.
type ctlHub struct{ d *Daemon }

var _ web.Control = ctlHub{}

// ctlWatch is one web.Control.Watch.
type ctlWatch struct {
	changed func()
	exited  func(pane string, code int)
}

func (h ctlHub) State() model.State {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.d.snapshot()
}

func (h ctlHub) Read(pane string, screen bool) (string, error) {
	r := h.d.read(proto.Read{Pane: pane, Screen: screen})
	if r.Err != "" {
		return "", errors.New(r.Err)
	}
	return r.Text, nil
}

func (h ctlHub) Scrollback(pane string, lines int) (string, error) {
	switch r := h.d.scrollback(proto.Scrollback{Pane: pane, Lines: lines}).(type) {
	case proto.ScrollbackResult:
		return r.Text, nil
	case proto.Error:
		return "", errors.New(r.Message)
	}
	return "", errors.New("no scrollback")
}

func (h ctlHub) Watch(changed func(), exited func(string, int)) func() {
	w := &ctlWatch{changed, exited}
	h.d.mu.Lock()
	if h.d.ctlWatch == nil {
		h.d.ctlWatch = map[*ctlWatch]bool{}
	}
	h.d.ctlWatch[w] = true
	h.d.mu.Unlock()
	return func() {
		h.d.mu.Lock()
		delete(h.d.ctlWatch, w)
		h.d.mu.Unlock()
	}
}
