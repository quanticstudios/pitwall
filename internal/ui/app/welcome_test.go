package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
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
