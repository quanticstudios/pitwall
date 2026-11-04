package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
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

func TestPathViewer(t *testing.T) {
	for _, name := range []string{"view.cmd", "view.BAT", `C:\tools\view.CmD`, "view.cmd. "} {
		if err := checkPathViewer(name); err == nil || !strings.Contains(err.Error(), ".exe") {
			t.Fatalf("batch viewer %q: %v", name, err)
		}
	}
	for _, name := range []string{"bat.exe", `C:\tools.cmd\view.exe`, "/bin/less"} {
		if err := checkPathViewer(name); err != nil {
			t.Fatalf("executable viewer %q: %v", name, err)
		}
	}
}

func TestPathPopupNotices(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, sock, stop := runDaemon(t, f, func(o *Options) {
		start := o.StartPane
		o.StartPane = func(c pane.Config) (Pane, error) {
			if len(c.Cmd) != 0 && c.Cmd[0] == "missing-viewer" {
				return nil, errors.New("viewer not found")
			}
			return start(c)
		}
	})
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	dir := t.TempDir()
	if e := cli.request(proto.NewSession{Cwd: dir}); e != "" {
		t.Fatal(e)
	}
	w := watch.waitState("the tab", func(s model.State) bool { return len(s.Panes) == 1 }).Workspaces[0]
	// The running test binary is an executable on every platform.
	cmd := []string{os.Args[0]}
	request := proto.OpenPopup{WorkspaceID: w.ID, Cmd: cmd, Path: "a.md"}
	if e := cli.request(request); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("viewer", func(s model.State) bool { return s.Workspaces[0].Popup != nil })
	plain := request
	plain.Path = ""
	if e := cli.request(plain); !strings.Contains(e, "popup open already") {
		t.Fatalf("plain popup refusal: %q", e)
	}
	if notice := d.state().Notice; notice != "" {
		t.Fatalf("plain popup sets a path notice: %q", notice)
	}
	if e := cli.request(request); !strings.Contains(e, "popup open already") {
		t.Fatalf("second popup: %q", e)
	}
	st = watch.waitState("refusal notice", func(s model.State) bool { return strings.Contains(s.Notice, "popup open already") })
	if e := cli.request(proto.DismissNotice{Notice: st.Notice}); e != "" {
		t.Fatal(e)
	}
	f.pane(1).code = 1
	f.pane(1).Close()
	st = watch.waitState("exit notice", func(s model.State) bool {
		return strings.Contains(s.Notice, "exited with status 1") && s.Workspaces[0].Popup == nil
	})
	if e := cli.request(proto.DismissNotice{Notice: st.Notice}); e != "" {
		t.Fatal(e)
	}
	watch.waitState("dismissed", func(s model.State) bool { return s.Notice == "" })
	request.Cmd = []string{"missing-viewer"}
	if e := cli.request(request); e == "" {
		t.Fatal("missing viewer started")
	}
	st = watch.waitState("start failure notice", func(s model.State) bool { return strings.Contains(s.Notice, "File not opened:") })
	cli.request(proto.DismissNotice{Notice: st.Notice})
	watch.waitState("dismissed", func(s model.State) bool { return s.Notice == "" })
	request.Cmd = cmd
	if e := cli.request(request); e != "" {
		t.Fatal(e)
	}
	st = watch.waitState("second viewer", func(s model.State) bool { return s.Workspaces[0].Popup != nil })
	id := st.Workspaces[0].Popup.Pane
	d.mu.Lock()
	d.pathPopups[id] = time.Now().Add(-500 * time.Millisecond)
	d.mu.Unlock()
	f.pane(2).Close()
	st = watch.waitState("normal exit", func(s model.State) bool { return s.Workspaces[0].Popup == nil })
	if st.Notice != "" {
		t.Fatalf("normal exit notice: %q", st.Notice)
	}
	if e := cli.request(request); e != "" {
		t.Fatal(e)
	}
	st = watch.waitState("late viewer", func(s model.State) bool { return s.Workspaces[0].Popup != nil })
	d.mu.Lock()
	d.pathPopups[st.Workspaces[0].Popup.Pane] = time.Now().Add(-time.Minute)
	d.mu.Unlock()
	f.pane(3).code = 7
	f.pane(3).Close()
	st = watch.waitState("late failure notice", func(s model.State) bool {
		return strings.Contains(s.Notice, "exited with status 7") && s.Workspaces[0].Popup == nil
	})
	if strings.Contains(st.Notice, "Use a viewer") {
		t.Fatalf("a failed viewer gets paging advice: %q", st.Notice)
	}
	d.mu.Lock()
	left := len(d.pathPopups)
	d.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d closed viewers remain", left)
	}
}

func TestPathPopupExitNotice(t *testing.T) {
	for _, c := range []struct {
		viewer  string
		code    int
		elapsed time.Duration
		want    string
	}{
		{"less", 0, 500 * time.Millisecond, ""},
		{"less", 0, 100 * time.Millisecond, ""},
		{"less", 0, 3 * time.Millisecond, "File viewer less exited with status 0."},
		{"less", 1, time.Minute, "File viewer less exited with status 1."},
		{"BAT.EXE", 0, 3 * time.Millisecond, "File viewer BAT.EXE exited with status 0."},
		{"viewer", 1, time.Minute, "File viewer viewer exited with status 1."},
		{"viewer", 0, 3 * time.Millisecond, "File viewer viewer exited with status 0. Use a viewer that waits, such as less or bat --paging=always."},
	} {
		if got := pathPopupExitNotice(c.viewer, c.code, c.elapsed); got != c.want {
			t.Errorf("%s, %d, %v: %q, want %q", c.viewer, c.code, c.elapsed, got, c.want)
		}
	}
}
