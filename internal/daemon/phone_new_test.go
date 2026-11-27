package daemon

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// The phone opens a tab through newTab, restarts its command once it has
// exited, and closes the tab.
func TestPhoneStartRestartClose(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, sock, stop := runDaemon(t, f, func(*Options) {})
	defer stop()
	watch := dial(t, sock, "watch")
	dir := t.TempDir()

	tab, pane, err := d.phoneStart(remote.NewTab{Dir: dir, Agent: "claude", Cmd: []string{"claude", "fix it"}})
	if err != nil || tab == "" || pane == "" {
		t.Fatalf("start: %q %q %v", tab, pane, err)
	}
	watch.waitState("the new tab", func(s model.State) bool { return len(s.Panes) == 1 })
	fp := f.pane(0)
	if !slices.Equal(fp.cfg.Cmd, []string{"claude", "fix it"}) || fp.cfg.Cwd != dir {
		t.Fatalf("pane runs %q in %s", fp.cfg.Cmd, fp.cfg.Cwd)
	}
	if err := d.phoneRestart(pane); err == nil {
		t.Fatal("restarted a running pane")
	}

	fp.code = 3
	fp.Close()
	watch.waitState("the pane exited", func(s model.State) bool { return len(s.Panes) == 1 && s.Panes[0].Exited })
	if err := d.phoneRestart(pane); err != nil {
		t.Fatal(err)
	}
	st := watch.waitState("the pane runs again", func(s model.State) bool { return len(s.Panes) == 1 && !s.Panes[0].Exited })
	if st.Panes[0].ID != pane || !st.Panes[0].Held {
		t.Fatalf("after restart: %+v", st.Panes[0])
	}
	if again := f.pane(1); !slices.Equal(again.cfg.Cmd, []string{"claude", "fix it"}) || again.cfg.Cwd != dir {
		t.Fatalf("restart runs %q in %s", again.cfg.Cmd, again.cfg.Cwd)
	}

	if err := d.phoneCloseTab("nope"); err == nil {
		t.Fatal("closed a tab that does not exist")
	}
	if err := d.phoneCloseTab(tab); err != nil {
		t.Fatal(err)
	}
	watch.waitState("the tab closed", func(s model.State) bool { return len(s.Workspaces) == 0 })
}
