package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestDialogTakesKeys: with a dialog open, Ctrl+Shift+L does not open the
// agent panel behind it, and Escape still closes the dialog.
func TestDialogTakesKeys(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	u.modal.open(modalAddProject, "")
	keys(press("L", key.ModCtrl|key.ModShift))
	if u.nav.panelOpen {
		t.Fatal("Ctrl+Shift+L opened the panel behind the dialog")
	}
	keys(press(key.NameEscape, 0))
	if u.modal.kind != modalNone {
		t.Fatal("Escape left the dialog open")
	}
	keys(press("L", key.ModCtrl|key.ModShift))
	if !u.nav.panelOpen {
		t.Fatal("Ctrl+Shift+L did nothing once the dialog closed")
	}
}

// TestWelcomeHidesHooksNotice: the first run asks to install hooks once,
// on the welcome card; the pane notice waits until the card is gone.
func TestWelcomeHidesHooksNotice(t *testing.T) {
	defer func(f func(bool, bool) (string, error)) { InstallHooks = f }(InstallHooks)
	InstallHooks = func(bool, bool) (string, error) { return "", nil }
	u, _ := keyWindow(t, conventional)
	b := u.b.(*FakeBackend)
	b.mu.Lock()
	b.st.Panes[0].HooksMissing = true
	pane := b.st.Panes[0].ID
	b.mu.Unlock()
	frame := func() {
		var ops op.Ops
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()})
	}
	u.welcome.on, u.hooks.shownAt = true, time.Time{}
	frame()
	if !u.hooks.shownAt.IsZero() {
		t.Fatal("the pane notice showed under the welcome card")
	}
	u.welcome.on = false
	frame()
	if u.hooks.shownAt.IsZero() {
		t.Fatalf("no pane notice for %s after the welcome card", pane)
	}
}
