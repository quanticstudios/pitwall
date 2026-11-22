package daemon

// The daemon's side of the control commands on the JSON socket
// (internal/web/control.go): copies of the state, pane text, notices of
// changes and pane exits, and the requests that start and drive tabs.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
	"github.com/quanticstudios/pitwall/internal/web"
)

// ctlHub is the daemon as web.Control.
type ctlHub struct{ d *Daemon }

var (
	_ web.Control            = ctlHub{}
	_ web.ControlFormatter   = ctlHub{}
	_ web.ControlKeys        = ctlHub{}
	_ web.ControlEventSource = ctlHub{}
)

// ctlWatch is one web.Control.Watch.
type ctlWatch struct {
	changed func()
	exited  func(pane string, code int)
}

// ctlEventSub is one web.ControlEventSource.WatchEvents subscriber.
type ctlEventSub struct {
	fn func(web.ControlEvent)
}

func (h ctlHub) State() model.State {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	s := h.d.snapshot()
	s.Starts = maps.Clone(h.d.st.Starts) // newTab matches retries against it
	return s
}

func (h ctlHub) Read(pane string, screen bool) (string, error) {
	r := h.d.read(proto.Read{Pane: pane, Screen: screen})
	if r.Err != "" {
		return "", errors.New(r.Err)
	}
	return r.Text, nil
}

func (h ctlHub) ReadFormat(pane string, screen bool, format string) (string, error) {
	if !screen {
		if format != "" && format != vt.FormatText {
			return "", web.ControlError("invalid", "format ansi or html requires screen: true")
		}
		return h.Read(pane, false)
	}
	h.d.mu.Lock()
	p := h.d.panes[pane]
	h.d.mu.Unlock()
	if p == nil {
		return "", web.ControlError("not-found", fmt.Sprintf("no pane %s", pane))
	}
	text, err := vt.FormatGrid(p.Snapshot(), format, vt.CurrentPalette())
	if err != nil {
		return "", web.ControlError("invalid", err.Error())
	}
	return text, nil
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

func (h ctlHub) ScrollbackFormat(pane string, lines int, format string) (string, error) {
	if format == "" || format == vt.FormatText {
		return h.Scrollback(pane, lines)
	}
	if lines < 0 {
		return "", web.ControlError("invalid", "lines must be non-negative")
	}
	h.d.mu.Lock()
	p := h.d.panes[pane]
	h.d.mu.Unlock()
	if p == nil {
		return "", web.ControlError("not-found", fmt.Sprintf("no pane %s", pane))
	}
	pal := vt.CurrentPalette()
	if sf, ok := p.(interface {
		ScrollbackFormatted(format string, lines int, pal vt.Palette) (string, error)
	}); ok {
		text, err := sf.ScrollbackFormatted(format, lines, pal)
		if err != nil {
			return "", web.ControlError("invalid", err.Error())
		}
		return text, nil
	}
	// Fallback for test fakes without ScrollbackFormatted: format the snapshot.
	text, err := vt.FormatGrid(p.Snapshot(), format, pal)
	if err != nil {
		return "", web.ControlError("invalid", err.Error())
	}
	return text, nil
}

func (h ctlHub) SendKeys(ctx context.Context, pane string, keys []string) error {
	h.d.mu.Lock()
	st := h.d.snapshot()
	p := h.d.panes[pane]
	alive := p != nil && h.d.inputs[pane] != nil
	var tabID string
	if i := slices.IndexFunc(st.Panes, func(sp model.Pane) bool { return sp.ID == pane }); i >= 0 {
		tabID = st.Panes[i].WorkspaceID
	}
	h.d.mu.Unlock()
	if !alive {
		return web.ControlError("not-found", fmt.Sprintf("no running pane %s", pane))
	}
	if tabID != "" && control.Guarded(st, tabID) {
		return web.ControlError("refused", "the tab is guarded: sendKey cannot send keys to a guarded tab")
	}
	for _, k := range keys {
		data, err := input.EncodeKey(k, p.Modes())
		if err != nil {
			return web.ControlError("invalid", err.Error())
		}
		if err := h.d.handle(ctx, proto.Input{Pane: pane, Data: data}); err != nil {
			return ctlFail(err)
		}
	}
	return nil
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

func (h ctlHub) WatchEvents(fn func(web.ControlEvent)) func() {
	sub := &ctlEventSub{fn: fn}
	h.d.mu.Lock()
	if h.d.ctlEvents == nil {
		h.d.ctlEvents = map[*ctlEventSub]bool{}
	}
	h.d.ctlEvents[sub] = true
	h.d.mu.Unlock()
	return func() {
		h.d.mu.Lock()
		delete(h.d.ctlEvents, sub)
		h.d.mu.Unlock()
	}
}

// emitEventLocked sends a direct ControlEvent to all subscribeEvents watchers.
// Callers hold d.mu; subscriber callbacks never block.
func (d *Daemon) emitEventLocked(ev web.ControlEvent) {
	if len(d.ctlEvents) == 0 {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Tab == "" && ev.Pane != "" {
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == ev.Pane }); i >= 0 {
			ev.Tab = d.st.Panes[i].WorkspaceID
		}
	}
	if ev.Session == "" && ev.Tab != "" {
		ev.Session = d.st.SessionOf(ev.Tab)
	}
	if ev.SessionName == "" && ev.Session != "" {
		if s := d.st.Session(ev.Session); s != nil {
			ev.SessionName = s.Name
		}
	}
	if ev.Parent == "" && ev.Tab != "" {
		if w := d.workspace(ev.Tab); w != nil {
			ev.Parent = w.Parent
		}
	}
	for sub := range d.ctlEvents {
		sub.fn(ev)
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

// hostCheckTimeout bounds the entire JSON check, including local git config
// reads and guard probes, rather than only its calls to origin and GitHub.
var hostCheckTimeout = 30 * time.Second

func (h ctlHub) CheckStart(ctx context.Context, m proto.HostCheck) []proto.HostProblem {
	ctx, cancel := context.WithTimeout(ctx, hostCheckTimeout)
	defer cancel()
	problems := checkStart(ctx, m, false)
	if err := ctx.Err(); err != nil {
		return []proto.HostProblem{{What: "host check did not finish: " + err.Error(), Fix: "retry the check"}}
	}
	return problems
}
