package daemon

import (
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
)

type exportPane struct {
	Pane
	text string
}

func (p exportPane) Scrollback() string { return p.text }

func TestScrollbackLines(t *testing.T) {
	d, f := talkDaemon(t)
	defer f.pane(0).Close()
	defer f.pane(1).Close()
	d.mu.Lock()
	d.panes["a"] = exportPane{Pane: d.panes["a"], text: "first\n\nlast\n"}
	d.mu.Unlock()
	for _, tc := range []struct {
		lines int
		want  string
	}{{0, "first\n\nlast\n"}, {1, "last\n"}, {2, "\nlast\n"}, {3, "first\n\nlast\n"}, {99, "first\n\nlast\n"}} {
		r, ok := d.scrollback(proto.Scrollback{Pane: "a", Lines: tc.lines}).(proto.ScrollbackResult)
		if !ok || r.Pane != "a" || r.Text != tc.want {
			t.Fatalf("lines %d: %+v", tc.lines, r)
		}
	}
	for _, m := range []proto.Scrollback{{Pane: "missing"}, {Pane: "a", Lines: -1}, {Pane: "b"}} {
		if _, ok := d.scrollback(m).(proto.Error); !ok {
			t.Errorf("no error for %+v", m)
		}
	}
}

func TestEditorCommand(t *testing.T) {
	for _, tc := range []struct {
		visual, editor string
		want           []string
	}{
		{"", "", []string{"vi"}},
		{"  ", "nano -w", []string{"nano", "-w"}},
		{`"/an editor/bin/vim" -R`, "nano", []string{"/an editor/bin/vim", "-R"}},
	} {
		got, err := editorCommand(tc.visual, tc.editor)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("got %q, %v, want %q", got, err, tc.want)
		}
	}
	for _, bad := range []string{`"unfinished`, `""`} {
		if _, err := editorCommand(bad, "vi"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestEditScrollbackCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "start fails"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(k, dir)
			}
			t.Setenv("VISUAL", `"editor with spaces" -R`)
			t.Setenv("EDITOR", "wrong-editor")
			d, f := talkDaemon(t)
			defer f.pane(0).Close()
			defer f.pane(1).Close()
			d.mu.Lock()
			d.panes["a"] = exportPane{Pane: d.panes["a"], text: "history\nscreen\n"}
			start := d.o.StartPane
			var cfg pane.Config
			d.o.StartPane = func(c pane.Config) (Pane, error) {
				cfg = c
				data, err := os.ReadFile(c.Cmd[len(c.Cmd)-1])
				if err != nil || string(data) != "history\nscreen\n" {
					t.Errorf("file %q, %v", data, err)
				}
				info, err := os.Stat(c.Cmd[len(c.Cmd)-1])
				if err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
					t.Errorf("file permissions: %v, %v", info, err)
				}
				if fail {
					return nil, errors.New("editor not found")
				}
				return start(c)
			}
			d.mu.Unlock()
			err := d.editScrollback(proto.EditScrollback{Pane: "a"})
			if (err != nil) != fail {
				t.Fatalf("edit: %v", err)
			}
			if !reflect.DeepEqual(cfg.Cmd[:2], []string{"editor with spaces", "-R"}) {
				t.Fatalf("command %q", cfg.Cmd)
			}
			path := cfg.Cmd[len(cfg.Cmd)-1]
			if !fail {
				d.mu.Lock()
				for _, p := range d.st.Panes {
					if p.ID == cfg.ID && p.ScrollbackFile != path {
						t.Errorf("editor has no saved temporary path: %+v", p)
					}
				}
				d.mu.Unlock()
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
				f.pane(2).Close()
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, err := os.Stat(path); os.IsNotExist(err) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("scrollback file survives editor exit")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestScrollbackProtocolLevel(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Since(proto.Scrollback{}) - 1, Kind: "cli"})
	if err := c.request(proto.Scrollback{Pane: "missing"}); !strings.Contains(err, "protocol level 56") {
		t.Fatalf("old client: %q", err)
	}
}

func TestScrollbackFrameLimit(t *testing.T) {
	d, f := talkDaemon(t)
	defer f.pane(0).Close()
	defer f.pane(1).Close()
	d.mu.Lock()
	d.panes["a"] = exportPane{Pane: d.panes["a"], text: strings.Repeat("x", 63<<20) + "\nlast\n"}
	d.mu.Unlock()
	if e, ok := d.scrollback(proto.Scrollback{Pane: "a"}).(proto.Error); !ok || !strings.Contains(e.Message, "use --lines") {
		t.Fatal("oversized scrollback needs a clear error")
	}
	if r, ok := d.scrollback(proto.Scrollback{Pane: "a", Lines: 1}).(proto.ScrollbackResult); !ok || r.Text != "last\n" {
		t.Fatalf("limited reply: %+v", r)
	}
}

func TestClosedSkipsScrollbackEditor(t *testing.T) {
	d := &Daemon{}
	w := model.Workspace{ID: "w"}
	editor := model.Pane{ID: "editor", WorkspaceID: "w", Cmd: []string{"nvim", "/tmp/scrollback"}, ScrollbackFile: "/tmp/scrollback"}
	d.recordClosed(w, []model.Pane{editor})
	if len(d.st.Closed) != 0 {
		t.Fatal("closed history retains an editor on a temporary path")
	}
	d.recordClosed(w, []model.Pane{editor, {ID: "shell", WorkspaceID: "w"}})
	if len(d.st.Closed) != 1 || d.st.Closed[0].Pane.ID != "shell" {
		t.Fatal("closed history must choose the normal pane")
	}
}
