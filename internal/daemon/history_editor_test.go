//go:build unix

package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestRestoredHistoryEditorExport(t *testing.T) {
	for _, tc := range []struct {
		name        string
		held, ports bool
	}{
		{"live", false, false}, {"held", true, false}, {"ports", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := tc.held
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, t.TempDir())
			}
			t.Setenv("VISUAL", "test-editor -R")
			st := restoreState(t)
			st.Panes, st.Workspaces, st.Sessions = st.Panes[:1], st.Workspaces[:1], st.Sessions[:1]
			if tc.ports {
				st.Workspaces[0].Ports = model.PortBlock{First: 41000, Last: 41002}
			}
			sp := &st.Panes[0]
			sp.Provider, sp.SessionID = "", ""
			sp.Held, sp.Exited = held, held
			if held {
				sp.Cmd = []string{"build"}
			}
			e := vt.New(80, 24, io.Discard)
			defer e.(io.Closer).Close()
			for row := range 40 {
				fmt.Fprintf(e, "\x1b[32msaved row %02d: 界 café\x1b[0m\r\n", row)
			}
			cols, text := vt.HistoryText(e, 2000)
			var err error
			sp.History, err = store.PackHistory(cols, text, time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "state.json")
			if err := store.Save(path, st); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			f := &fakes{saved: loaded}
			o := f.options()
			o.NewVT, o.PersistScrollback = vt.New, func() int { return 2000 }
			o.RestoreCmd = func(model.Pane) []string {
				return []string{"sh", "-c", `printf '\033]133;A\007$ \033]133;B\007echo fresh output\r\n\033]133;C\007'
n=0
while [ "$n" -lt 30 ]; do printf 'fresh output %02d\n' "$n"; n=$((n+1)); done
printf '\033]133;D;0\007\033]133;A\007$ \033]133;B\007'
exec cat >/dev/null`}
			}
			fakeStart := o.StartPane
			var editorText string
			o.StartPane = func(c pane.Config) (Pane, error) {
				if c.ID == sp.ID {
					p, err := pane.Start(c)
					if err != nil {
						return nil, err
					}
					return p, nil
				}
				data, err := os.ReadFile(c.Cmd[len(c.Cmd)-1])
				if err != nil {
					return nil, err
				}
				editorText = string(data)
				return fakeStart(c)
			}
			d, err := NewWith(o)
			if err != nil {
				t.Fatal(err)
			}
			defer d.shutdown()
			marker := "fresh output"
			if held {
				marker = "before pitwall restarted"
			}
			var exported string
			waitUntil(t, "restored output", func() bool {
				exported, err = d.scrollbackText(sp.ID)
				if held {
					return err == nil && strings.Contains(exported, marker)
				}
				blocks := d.commands(proto.Commands{Pane: sp.ID}).Blocks
				return err == nil && len(blocks) == 1 && blocks[0].Finished && strings.Contains(exported, "fresh output 29") && strings.HasSuffix(exported, "$\n")
			})
			if strings.Contains(exported, "\x1b") || strings.Count(exported, "Restored from") != 1 {
				t.Fatalf("plain export: %q", exported)
			}
			for row := range 40 {
				if want := fmt.Sprintf("saved row %02d: 界 café", row); !strings.Contains(exported, want) {
					t.Fatalf("export lost %q", want)
				}
			}
			d.mu.Lock()
			handle := d.panes[sp.ID]
			d.mu.Unlock()
			shell, ok := handle.(interface {
				Commands() ([]vt.CommandBlock, bool)
				HasPromptMarks() bool
			})
			if !ok {
				t.Fatal("pane lost its shell integration methods")
			}
			blocks, integrated := shell.Commands()
			if held {
				if integrated || shell.HasPromptMarks() || len(blocks) != 0 {
					t.Fatal("restored plain text invents shell marks")
				}
				if err := d.scroll(proto.Scroll{Pane: sp.ID, Prompts: 1}); err == nil {
					t.Fatal("unmarked history allows a prompt jump")
				}
			} else {
				if !integrated || !shell.HasPromptMarks() || len(blocks) != 1 || blocks[0].Command != "echo fresh output" || !strings.Contains(blocks[0].Output, "fresh output 29") {
					t.Fatalf("commands after restore: %+v", blocks)
				}
				if err := d.scroll(proto.Scroll{Pane: sp.ID, Prompts: 1}); err != nil {
					t.Fatal(err)
				}
				d.mu.Lock()
				frame := d.frame(sp.ID, handle)
				d.mu.Unlock()
				var top strings.Builder
				for _, cell := range frame.Grid.Cells[:frame.Grid.Cols] {
					top.WriteString(cell.Content)
				}
				if !frame.PromptMarks || frame.ScrollOffset == 0 || !strings.Contains(top.String(), "$ echo fresh output") {
					t.Fatalf("prompt jump after restore: offset %d, top %q", frame.ScrollOffset, top.String())
				}
			}
			d.Save()
			f.mu.Lock()
			saved := f.saved.Panes[0].History
			f.mu.Unlock()
			persisted, err := store.UnpackHistory(saved)
			if err != nil {
				t.Fatal(err)
			}
			plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(strings.ReplaceAll(persisted, "\r", ""), "")
			if !held && strings.TrimRight(plain, "\n") != strings.TrimRight(exported, "\n") {
				t.Fatalf("capture and export differ: %q, %q", plain, exported)
			}
			if held && !strings.HasPrefix(exported, strings.TrimRight(plain, "\n")) {
				t.Fatalf("held export lost saved source: %q, %q", plain, exported)
			}
			if err := d.editScrollback(proto.EditScrollback{Pane: sp.ID}); err != nil {
				t.Fatal(err)
			}
			if editorText != exported {
				t.Fatalf("editor differs from export: %q, %q", editorText, exported)
			}
		})
	}
}
