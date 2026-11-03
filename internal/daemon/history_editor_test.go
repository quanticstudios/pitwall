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
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, t.TempDir())
			}
			t.Setenv("VISUAL", "test-editor -R")
			st := restoreState(t)
			st.Panes, st.Workspaces, st.Sessions = st.Panes[:1], st.Workspaces[:1], st.Sessions[:1]
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
			o.RestoreCmd = func(model.Pane) []string { return []string{"sh", "-c", "printf 'fresh output\n'; exec cat >/dev/null"} }
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
				return err == nil && strings.Contains(exported, marker)
			})
			if strings.Contains(exported, "\x1b") || strings.Count(exported, "Restored from") != 1 {
				t.Fatalf("plain export: %q", exported)
			}
			for row := range 40 {
				if want := fmt.Sprintf("saved row %02d: 界 café", row); !strings.Contains(exported, want) {
					t.Fatalf("export lost %q", want)
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
