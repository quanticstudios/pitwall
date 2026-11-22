package app

import (
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A first run, with no state, config or gui.json yet, shows the card; the
// window's own bookkeeping keeps it, the first thing the user does hides it
// for good. A user with saved state never sees it.
func TestWelcome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	NoteFirstRun()
	if !loadGUIState().Welcome {
		t.Fatal("first run: no welcome")
	}
	NoteFirstRun()
	u := &ui{b: NewFakeBackend(), gui: loadGUIState()}
	u.welcome.on = u.gui.Welcome
	if !u.welcome.on {
		t.Fatal("a second launch before any action lost the welcome")
	}
	u.send(proto.Resize{Pane: "a", Cols: 80, Rows: 24})
	u.send(proto.SessionShow{SessionID: "s1"})
	u.send(proto.Input{Pane: "a", Data: []byte("\x1b[I")})
	if !u.welcome.on {
		t.Fatal("the window's own messages hid the welcome")
	}
	u.send(proto.Input{Pane: "a", Data: []byte("l")})
	if u.welcome.on || loadGUIState().Welcome {
		t.Fatal("typing in a pane kept the welcome")
	}
	NoteFirstRun()
	if loadGUIState().Welcome {
		t.Fatal("the welcome came back")
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(store.Path()), 0o755)
	os.WriteFile(store.Path(), []byte("{}"), 0o600)
	NoteFirstRun()
	if loadGUIState().Welcome {
		t.Fatal("a user with saved state got the welcome")
	}
}

// exeName is the file name exec.LookPath finds for a command: Windows
// looks for it with an extension from PATHEXT.
func exeName(cmd string) string {
	if runtime.GOOS == "windows" {
		return cmd + ".exe"
	}
	return cmd
}

func TestFindAgents(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	for _, name := range []string{"codex", "claude", "aider", "notanagent"} {
		os.WriteFile(filepath.Join(bin, exeName(name)), []byte("#!/bin/sh\nsleep 1000\n"), 0o755)
	}
	t.Setenv("PATH", bin)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	got := findAgents(home)
	if len(got) != 3 || got[0].cmd != "claude" || got[1].cmd != "codex" || got[2].cmd != "aider" || got[0].hooks || got[1].hooks || got[0].hookless || !got[2].hookless {
		t.Fatalf("findAgents = %+v", got)
	}
}

// TestDrawNoAgentsFitsDefaultWindow verifies the multi-line no-agents card
// renders at the default 1280x800 pane area (991dp wide with 288dp sidebar)
// and narrower split widths, and that Dismiss still closes it.
func TestDrawNoAgentsFitsDefaultWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, w := range []int{991, 640, 420} {
		u := &ui{b: NewFakeBackend(), th: theme.Dark()}
		u.welcome.on = true
		u.welcome.read = true // 0 agents on PATH
		st := u.b.State()
		var r input.Router
		var ops op.Ops
		gtx := gl.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(w, 800)),
			Now:         time.Now(),
		}
		u.drawWelcome(gtx, &st)
		r.Frame(&ops)
		if len(u.welcome.links) != len(welcomeAgents) {
			t.Fatalf("width %d: links not initialized", w)
		}
		// Clicking Dismiss on the rendered card hides the welcome state.
		u.welcome.dismiss.Click()
		ops.Reset()
		u.drawWelcome(gtx, &st)
		if u.welcome.on {
			t.Fatalf("width %d: Dismiss did not close no-agents card", w)
		}
	}
}

// TestWelcomeModalFocusAndEscape verifies that the welcome modal claims
// keyboard focus, swallows typing on its backdrop without leaking to an
// underlying pane target, and dismisses on Escape.
func TestWelcomeModalFocusAndEscape(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, th: theme.Dark(), nav: nav{keys: conventional}}
	u.nav.sync(&st)
	u.welcome.on = true
	u.welcome.read = true
	if u.welcome.modal() {
		t.Fatal("no-agents card reported modal=true")
	}
	u.welcome.agents = []foundAgent{{cmd: "claude", name: "Claude Code", hooks: true}}
	u.welcome.start = make([]widget.Clickable, len(u.welcome.agents))
	if !u.welcome.modal() {
		t.Fatal("welcome card with agents reported modal=false")
	}

	var r input.Router
	var ops op.Ops
	var underneath int
	var leaked []event.Event
	frame := func() {
		ops.Reset()
		gtx := gl.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(991, 800)),
			Now:         time.Now(),
		}
		for {
			ev, ok := gtx.Event(key.Filter{Focus: &underneath, Optional: key.ModShift})
			if !ok {
				break
			}
			leaked = append(leaked, ev)
		}
		event.Op(gtx.Ops, &underneath)
		u.drawWelcome(gtx, &st)
		r.Frame(&ops)
	}
	frame()
	frame()
	if !r.Source().Focused(&u.welcome.backdrop) {
		t.Fatal("welcome modal did not take keyboard focus")
	}

	r.Queue(key.EditEvent{Text: "ls"}, key.Event{Name: key.NameReturn, State: key.Press})
	frame()
	frame()
	if !u.welcome.on {
		t.Fatal("typing dismissed welcome modal")
	}
	if len(leaked) != 0 {
		t.Fatalf("keys leaked to underlying target: %+v", leaked)
	}

	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	frame()
	frame()
	if u.welcome.on {
		t.Fatal("Escape did not dismiss welcome modal")
	}
	if len(leaked) != 0 {
		t.Fatalf("Escape leaked to underlying target: %+v", leaked)
	}
}
