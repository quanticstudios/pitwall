package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestPopupPath(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if e := cli.request(proto.NewSession{Cwd: dir}); e != "" {
		t.Fatal(e)
	}
	w := watch.waitState("the tab", func(s model.State) bool { return len(s.Panes) == 1 }).Workspaces[0]
	paths := []string{"my file.md", "--help", "$(touch injected); `echo bad` ' \".md", "line\nbreak.md", "~/notes.md", filepath.Join(dir, "abs.md")}
	for i, path := range paths {
		cmd := []string{os.Args[0], "-test.run=^TestPopupPathArgument$", "--"}
		if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Cmd: cmd, Dir: dir, Path: path}); e != "" {
			t.Fatal(e)
		}
		st := watch.waitState("the popup", func(s model.State) bool { return s.Workspaces[0].Popup != nil })
		want := filepath.Join(dir, path)
		if filepath.IsAbs(path) {
			want = path
		} else if path == "~/notes.md" {
			want = filepath.Join(home, "notes.md")
		}
		fp := f.pane(i + 1)
		if !slices.Equal(fp.cfg.Cmd, append(slices.Clone(cmd), want)) || fp.cfg.Cwd != dir {
			t.Fatalf("%q: %q in %q", path, fp.cfg.Cmd, fp.cfg.Cwd)
		}
		process := exec.Command(fp.cfg.Cmd[0], fp.cfg.Cmd[1:]...)
		process.Env = append(os.Environ(), "PITWALL_PATH_ARGUMENT_TEST=1")
		if out, err := process.Output(); err != nil || string(out) != want {
			t.Fatalf("process got %q, %v; want %q", out, err, want)
		}
		if e := cli.request(proto.ClosePane{Pane: st.Workspaces[0].Popup.Pane}); e != "" {
			t.Fatal(e)
		}
		watch.waitState("closed", func(s model.State) bool { return s.Workspaces[0].Popup == nil })
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatalf("injected command ran: %v", err)
	}
	if e := cli.request(proto.OpenPopup{WorkspaceID: w.ID, Path: "--help"}); e == "" {
		t.Fatal("path without a viewer opened a shell")
	}
}

func TestPopupPathArgument(t *testing.T) {
	if os.Getenv("PITWALL_PATH_ARGUMENT_TEST") == "1" {
		fmt.Print(os.Args[len(os.Args)-1])
		os.Exit(0)
	}
}
