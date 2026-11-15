package daemon

// The daemon's side of the control commands on the JSON socket
// (internal/web/control.go): copies of the state, pane text, notices of
// changes and pane exits, and the requests that start and drive tabs.

import (
	"context"
	"errors"
	"io/fs"

	"github.com/quanticstudios/pitwall/internal/config"
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

func (h ctlHub) Settings() config.Settings {
	if h.d.o.Fleet == nil {
		return config.Settings{}
	}
	return h.d.o.Fleet()
}

func (h ctlHub) Do(ctx context.Context, req any) error {
	if m, ok := req.(proto.Send); ok {
		// A JSON script may omit From. Bound its queued sends too, across
		// connections, without changing the CLI's script loop exemption.
		err := h.d.sendMessageLimit(m, "", false, sendLimit)
		if err == nil && m.From == "" {
			h.d.recordPrompt(m.Pane)
		}
		return ctlFail(err)
	}
	return ctlFail(h.d.handle(ctx, req))
}

func (h ctlHub) OpenPane(m proto.OpenPane, env []string) (string, error) {
	id, err := h.d.openPaneEnv(m, "", env)
	return id, ctlFail(err)
}

func (h ctlHub) NewTab(ctx context.Context, m proto.NewTab) (string, error) {
	id := newID()
	return id, ctlFail(h.d.newTabID(ctx, m, id))
}

// ctlFail is err with the control result code that says what failed.
func ctlFail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errUnguarded):
		return web.ControlError("refused", err.Error())
	case errors.Is(err, errSendFull):
		return web.ControlError("busy", err.Error())
	case errors.Is(err, fs.ErrNotExist):
		return web.ControlError("not-found", err.Error())
	}
	return web.ControlError("conflict", err.Error())
}
