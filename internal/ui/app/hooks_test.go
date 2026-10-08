package app

import (
	"image"
	"reflect"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestHookChanges(t *testing.T) {
	files, changes, changed := hookChanges("/h/.claude/settings.json: added Stop: '/bin/pitwall' hook claude\n" +
		"/h/.claude/settings.json: added SessionStart: '/bin/pitwall' hook claude\n" +
		"/h/.codex/hooks.json: unchanged\n" +
		"/h/.config/opencode/plugins/pitwall.js: skipped: edited since pitwall wrote it\n")
	if !changed || !reflect.DeepEqual(files, []string{"/h/.claude/settings.json", "/h/.codex/hooks.json", "/h/.config/opencode/plugins/pitwall.js"}) ||
		!reflect.DeepEqual(changes["/h/.claude/settings.json"], []string{"added Stop", "added SessionStart"}) {
		t.Fatalf("got %v %v %v", files, changes, changed)
	}
	if _, _, changed := hookChanges("/h/.codex/hooks.json: unchanged\n/h/x.js: skipped: edited\n"); changed {
		t.Fatal("unchanged and skipped files count as a change")
	}
}

// The dialog shows the dry run first and installs only on confirm; with
// nothing to change, confirm closes it. An install hides the pane notices.
func TestHooksDialog(t *testing.T) {
	for _, tc := range []struct {
		name string
		dry  string
		want []bool
	}{
		{"changes", "/h/.claude/settings.json: added Stop: 'x' hook claude\n/h/.codex/hooks.json: unchanged\n", []bool{true, false}},
		{"nothing to do", "/h/.claude/settings.json: unchanged\n", []bool{true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []bool
			old := InstallHooks
			InstallHooks = func(dry bool) (string, error) {
				mu.Lock()
				calls = append(calls, dry)
				mu.Unlock()
				if dry {
					return tc.dry, nil
				}
				return "Backup: /h/.claude/settings.json.pitwall-backup-1\n", nil
			}
			t.Cleanup(func() { InstallHooks = old })
			u := &ui{th: theme.Dark()}
			idle := func() {
				t.Helper()
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					u.hooks.mu.Lock()
					busy := u.hooks.busy
					u.hooks.mu.Unlock()
					if !busy {
						return
					}
				}
				t.Fatal("InstallHooks never answered")
			}
			var r input.Router
			var ops op.Ops
			frame := func() {
				ops.Reset()
				gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
					Constraints: gl.Exact(image.Pt(800, 800)), Now: time.Now()}
				u.layoutModal(gtx, &model.State{})
				r.Frame(&ops)
			}
			if !u.hooksNotice() {
				t.Fatal("notice hidden before an install")
			}
			u.openHooks()
			idle()
			frame()
			u.confirmHooks()
			idle()
			frame()
			if len(tc.want) == 2 {
				if u.modal.kind != modalHooks || u.hooksNotice() {
					t.Fatalf("after the install: dialog %v, notice %v", u.modal.kind, u.hooksNotice())
				}
				u.confirmHooks()
			}
			mu.Lock()
			defer mu.Unlock()
			if u.modal.kind != modalNone || !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("dialog %v, calls %v, want %v", u.modal.kind, calls, tc.want)
			}
		})
	}
}
