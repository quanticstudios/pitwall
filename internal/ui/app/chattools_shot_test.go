package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestToolsMenuShots writes headless PNGs of the chat header's Tools
// dropdown to $PITWALL_TOOLS_SHOTS for visual review; skips without it.
func TestToolsMenuShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOLS_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOLS_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	size := image.Pt(1040, 480)
	render := func(name string, th *theme.Theme, mode string, open bool, sel int) {
		b := shotBackend{NewFakeBackend(), model.ProviderClaude}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Chat tools dropdown", Path: t.TempDir(), Chat: "c1", Branch: "ui-dropdowns"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateReady, Title: "Chat tools dropdown"}}
		u := &ui{b: b, th: th, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		u.cfg.Chat.ToolCalls = mode
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wc", "")

		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		now := time.Now()
		for i := range 5 {
			if i == 2 && open {
				u.chatHead.setTools(true, now)
				u.chatHead.toolsSel = sel
			}
			ops.Reset()
			now = now.Add(200 * time.Millisecond)
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(size), Now: now})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	render("after-tools-open", theme.Dark(), config.ToolsAll, true, 0)
	render("after-tools-failed", theme.Dark(), config.ToolsFailed, true, 2)
	lt, _ := config.Builtin("aide-light")
	light, _ := theme.New(lt, config.Font{})
	render("after-tools-light", light, config.ToolsFailed, true, 1)
}
