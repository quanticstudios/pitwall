package daemon

import (
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A popup runs its command over the tab's splits without touching them,
// refuses a second popup, closes when the command exits with the exit code
// sent to watchers, and is never saved.
func TestPopup(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	dir, other := t.TempDir(), t.TempDir()
	if e := cli.request(proto.NewSession{Cwd: dir}); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("the tab", func(s model.State) bool { return len(s.Panes) == 1 })
	w := st.Workspaces[0]
	before := w.Tabs[0].Layout.Pane

	const id = "00112233aabbccdd"
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Pane: id, Cmd: []string{"fzf"}, Width: "50%", Title: "pick"}); e != "" {
		t.Fatal(e)
	}
	st = watch.waitState("the popup", func(s model.State) bool { return len(s.Panes) == 2 })
	w = st.Workspaces[0]
	if w.Popup == nil || *w.Popup != (model.Popup{Pane: id, Width: "50%", Title: "pick"}) || w.Tabs[0].Layout.Pane != before {
		t.Fatalf("popup %+v, layout %+v", w.Popup, w.Tabs[0].Layout)
	}
	if fp := f.pane(1); !slices.Equal(fp.cfg.Cmd, []string{"fzf"}) || fp.cfg.Cwd != dir {
		t.Fatalf("popup runs %q in %s", fp.cfg.Cmd, fp.cfg.Cwd)
	}
	if !st.IsPopup(id) || st.PaneCount(w.ID) != 1 {
		t.Fatalf("IsPopup %v, PaneCount %d", st.IsPopup(id), st.PaneCount(w.ID))
	}
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Dir: other}); !strings.Contains(e, "popup open already") {
		t.Fatalf("second popup: %q", e)
	}
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Pane: "../x"}); e == "" {
		t.Fatal("a pane id that is not hex was taken")
	}

	// A shutdown now saves the tab without its popup.
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	fp := f.pane(1)
	fp.code = 7
	fp.Close()
	watch.waitFor("PaneExited", func(m any) bool { return m == proto.PaneExited{Pane: id, ExitCode: 7} })
	cli.send(proto.Sync{}) // the watch may have had the state before the PaneExited
	st = cli.waitState("the popup closed", func(s model.State) bool { return len(s.Panes) == 1 })
	if st.Workspaces[0].Popup != nil || st.Panes[0].ID != before {
		t.Fatalf("after exit: popup %+v, panes %+v", st.Workspaces[0].Popup, st.Panes)
	}

	// Another one opens in --dir, and a ClosePane closes it.
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Dir: other}); e != "" {
		t.Fatal(e)
	}
	st = watch.waitState("the second popup", func(s model.State) bool { return len(s.Panes) == 2 })
	if f.pane(2).cfg.Cwd != other {
		t.Fatalf("popup in %s, want %s", f.pane(2).cfg.Cwd, other)
	}
	pop := st.Workspaces[0].Popup.Pane
	stop()
	stopped = true
	if slices.ContainsFunc(f.saved.Panes, func(p model.Pane) bool { return p.ID == pop }) || len(f.saved.Panes) != 1 {
		t.Fatalf("saved panes %+v", f.saved.Panes)
	}
}

// Closing a pinned tab's popup closes the popup alone, which the pin allows.
func TestPopupClosePinned(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	if e := cli.request(proto.NewSession{Cwd: t.TempDir()}); e != "" {
		t.Fatal(e)
	}
	w := watch.waitState("the tab", func(s model.State) bool { return len(s.Panes) == 1 }).Workspaces[0]
	if e := cli.request(proto.SetTabOptions{WorkspaceID: w.ID, Pinned: true}); e != "" {
		t.Fatal(e)
	}
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID}); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("the popup", func(s model.State) bool { return len(s.Panes) == 2 })
	if e := cli.request(proto.ClosePane{Pane: st.Workspaces[0].Popup.Pane}); e != "" {
		t.Fatal(e)
	}
	watch.waitState("the popup closed", func(s model.State) bool { return len(s.Panes) == 1 && s.Workspaces[0].Popup == nil })
}
