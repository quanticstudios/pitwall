package daemon

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/tmuxshim"
)

func tmuxHarness(t *testing.T) (*Daemon, *fakes, string, func(...string) (string, error)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTYs")
	}
	f := &fakes{}
	d, _, stop := runDaemon(t, f, func(o *Options) { o.Split = layout.Split; o.Remove = layout.Remove })
	t.Cleanup(stop)
	if err := d.handle(context.Background(), proto.NewSession{Name: "team", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	lead := d.st.Panes[0].ID
	d.mu.Unlock()
	return d, f, lead, func(args ...string) (string, error) {
		return d.tmux(proto.Tmux{From: lead, Args: args, Env: []string{"PATH=/bin", "TMUX=/tmp/test,1,0", "PITWALL_SOCKET=/tmp/test"}})
	}
}

func TestTmuxPaneLifecycle(t *testing.T) {
	d, f, lead, run := tmuxHarness(t)
	out, err := run("split-window", "-dhP", "-l70%", "-F#{pane_id}", "-e", "EXAMPLE=hello", "--", "printf", "%s", "a b")
	if err != nil {
		t.Fatal(err)
	}
	worker := strings.TrimSpace(out)
	d.mu.Lock()
	p := d.st.Panes[1]
	w := *d.workspace(p.WorkspaceID)
	tree := w.Tabs[0].Layout
	d.mu.Unlock()
	if tree.Dir != layout.Horizontal || tree.Ratios[1] != .7 {
		t.Fatalf("split=%+v", tree)
	}
	if !slices.Equal(p.Cmd, []string{"printf", "%s", "a b"}) || !slices.Contains(p.Env, "EXAMPLE=hello") || !slices.Contains(p.Env, "TMUX_PANE="+worker) {
		t.Fatalf("pane command/environment: %+v", p)
	}
	if _, err := run("select-pane", "-t", worker, "-T", "researcher"); err != nil {
		t.Fatal(err)
	}
	d.setTitle(p.ID, f.pane(1), "shell title")
	if got, err := run("display-message", "-pt", worker, "#{pane_title}"); err != nil || got != "researcher\n" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := run("send-keys", "-t", worker, "-N2", "-l", "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for f.pane(1).got() != "EnterEnter" {
		if time.Now().After(deadline) {
			t.Fatal("input was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := run("respawn-pane", "-t", worker, "--", "cat"); err == nil {
		t.Fatal("respawn accepted live pane without -k")
	}
	gui := &client{frames: map[string]proto.Frame{}, wake: make(chan struct{}, 1)}
	d.mu.Lock()
	d.clients[gui] = struct{}{}
	d.mu.Unlock()
	t.Cleanup(func() { d.mu.Lock(); delete(d.clients, gui); d.mu.Unlock() })
	if _, err := run("respawn-pane", "-kt", worker, "--", "echo hello"); err != nil {
		t.Fatal(err)
	}
	gui.mu.Lock()
	frame, refreshed := gui.frames[p.ID]
	gui.mu.Unlock()
	if !refreshed || frame.Grid.At(0, 0).Content != "x" {
		t.Fatal("respawn did not refresh a quiet terminal")
	}
	if !slices.Equal(f.pane(2).cfg.Cmd, []string{"/bin/sh", "-c", "echo hello"}) {
		t.Fatal(f.pane(2).cfg.Cmd)
	}
	if got, err := run("list-panes", "-F", "#{pane_id}"); err != nil || got != tmuxshim.ID("%", lead)+"\n"+worker+"\n" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := run("capture-pane", "-p", "-t", worker, "-S-", "-E-"); err != nil || got != "x\n" {
		t.Fatalf("capture %q %v", got, err)
	}
	if _, err := run("rename-window", "renamed"); err != nil {
		t.Fatal(err)
	}
	if got, err := run("list-windows", "-F", "#{window_name}"); err != nil || got != "renamed\n" {
		t.Fatalf("%q %v", got, err)
	}
	for _, target := range []string{"", ":0.0", "0.0", tmuxshim.ID("%", lead)} {
		if _, err := run("display-message", "-t", target, "-p", "#{pane_id}"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run("kill-pane", "-t", worker); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	count := len(d.st.Panes)
	d.mu.Unlock()
	if count != 1 {
		t.Fatal(count)
	}
}

func TestTmuxRejectsBeforeMutation(t *testing.T) {
	d, _, lead, run := tmuxHarness(t)
	for _, args := range [][]string{
		{}, {"kill-server"}, {"split-window", "-Z"}, {"split-window", "-hv"},
		{"split-window", "-b"}, {"split-window", "-l", "100%"}, {"split-window", "-e", "INVALID"},
		{"split-window", "-P", "-F", "#{q:pane_id}"},
		{"send-keys", "-lH", "61"}, {"send-keys", "-N", "0", "a"}, {"send-keys", "-N", "10001", "a"},
		{"send-keys", "-H", "xyz"}, {"send-keys", "-N10000", strings.Repeat("x", 1000)},
		{"list-panes", "unexpected"}, {"display-message", "-p", "a", "b"},
		{"capture-pane"}, {"capture-pane", "-p", "-S", "invalid"},
		{"set-option", "not-a-real-option", "on"}, {"set-option", "remain-on-exit", "invalid"},
		{"resize-pane", "-x", "0%"}, {"select-layout", "nonexistent"},
		{"respawn-pane", "-kt", tmuxshim.ID("%", lead), "cat"},
		{"kill-pane", "-t", "other:0.0"}, {"kill-pane", "-t", "%unknown"},
	} {
		if _, err := run(args...); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	d.mu.Lock()
	count := len(d.st.Panes)
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("invalid commands changed panes: %d", count)
	}
	if _, err := d.tmux(proto.Tmux{From: "missing", Args: []string{"list-panes"}}); err == nil {
		t.Fatal("missing origin accepted")
	}
	if _, err := tmuxEnv(nil, []string{"SECRET=bad"}, nil, lead); err == nil {
		t.Fatal("invalid launcher environment accepted")
	}
	for _, extra := range []string{"=value", "BAD\x00=value", "KEY=bad\x00"} {
		if _, err := tmuxEnv(nil, nil, []string{extra}, lead); err == nil {
			t.Fatal("invalid -e accepted")
		}
	}
}

func TestTmuxRemainOnExit(t *testing.T) {
	for _, mode := range []string{"off", "on", "failed"} {
		for _, code := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s-%d", mode, code), func(t *testing.T) {
				d, f, _, run := tmuxHarness(t)
				worker, err := run("split-window", "-dP", "-F#{pane_id}", "cat")
				if err != nil {
					t.Fatal(err)
				}
				worker = strings.TrimSpace(worker)
				if _, err := run("set-option", "-pt", worker, "remain-on-exit", mode); err != nil {
					t.Fatal(err)
				}
				if _, err := run("respawn-pane", "-kt", worker, "cat"); err != nil {
					t.Fatal(err)
				}
				p := f.pane(2)
				p.code = code
				p.Close()
				held := mode == "on" || mode == "failed" && code != 0
				deadline := time.Now().Add(time.Second)
				for {
					d.mu.Lock()
					s := d.snapshot()
					d.mu.Unlock()
					found := false
					exited := false
					for _, sp := range s.Panes {
						if tmuxshim.ID("%", sp.ID) == worker {
							found = true
							exited = sp.Exited && sp.ExitCode == code
						}
					}
					if held && found && exited || !held && !found {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("held=%v, found=%v, exited=%v", held, found, exited)
					}
					time.Sleep(time.Millisecond)
				}
			})
		}
	}
}

func TestTmuxTargetsStayInTab(t *testing.T) {
	d, _, lead, run := tmuxHarness(t)
	if err := d.handle(context.Background(), proto.NewSession{Name: "unrelated", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	other := d.st.Panes[1]
	w := *d.workspace(other.WorkspaceID)
	d.mu.Unlock()
	for _, target := range []string{other.ID, tmuxshim.ID("%", other.ID), tmuxshim.ID("@", w.ID), "unrelated:0.0"} {
		if _, err := run("kill-pane", "-t", target); err == nil {
			t.Errorf("reached foreign target %q", target)
		}
	}
	if got, err := run("list-panes", "-a", "-F#{pane_id}"); err != nil || got != tmuxshim.ID("%", lead)+"\n" {
		t.Fatalf("%q %v", got, err)
	}
}
