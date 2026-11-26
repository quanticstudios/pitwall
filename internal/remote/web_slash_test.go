//go:build unix

package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestWebSlash runs the phone page's / menu in headless Chromium, which
// testdata/slash_drive.js types and taps on. A chat whose daemon sent no
// commands gets no menu; /re at the start lists every match, Down and
// Enter fill the second in; a /word mid-draft lists skills only and a
// tap, which keeps the box focused, replaces just that word; a built-in's
// name mid-draft, a path, and Codex mid-draft open nothing; Esc shuts the
// menu until the text changes; a row is a thumb's height. With
// PITWALL_SLASH_SHOTS set it writes both menus as PNGs there.
func TestWebSlash(t *testing.T) {
	p := newWebPage(t, "testdata/slash_drive.js")
	var got struct {
		Old, Start, Mid, BuiltinMid, Path, Reopened, CodexMid []string
		Hidden, Closed, Focused, Esc, EscStays, Empty         bool
		Sel, AfterEnter, AfterTap, Error                      string
		Caret, CodexStart                                     int
		RowHeight                                             float64
	}
	json.Unmarshal([]byte(p.drive(t, "#pane=c")), &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	want := got
	none := []string{}
	want.Old, want.Start, want.Mid, want.BuiltinMid, want.Path = none, []string{"/review", "/release", "/code-review"}, []string{"/release", "/code-review"}, none, none
	want.Reopened, want.CodexMid = []string{"/release"}, none
	want.Hidden, want.Closed, want.Focused, want.Esc, want.EscStays, want.Empty = false, true, true, true, true, true
	want.Sel, want.AfterEnter, want.AfterTap, want.Caret = "/release", "/release ", "please run /code-review and then push", 24
	if !reflect.DeepEqual(got, want) || got.CodexStart != 3 || got.RowHeight < 48 {
		t.Fatalf("page:\n%+v\nwant:\n%+v (codexStart 3, rowHeight 48 or more)", got, want)
	}

	dir := os.Getenv("PITWALL_SLASH_SHOTS")
	if dir == "" {
		return
	}
	for _, shot := range []string{"start", "mid"} {
		file := filepath.Join(dir, "web-slash-"+shot+".png")
		p.chromium(t, func(exited chan error) bool {
			select {
			case err := <-exited:
				exited <- err
				return err == nil
			case <-time.After(30 * time.Second):
				return false
			}
		}, "--screenshot="+file, "--window-size=400,760", "--force-device-scale-factor=2", "--hide-scrollbars", p.url+"#pane=c&shot="+shot)
		if _, err := os.Stat(file); err != nil {
			t.Fatal(err)
		}
	}
}
