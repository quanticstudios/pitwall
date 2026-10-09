package app

import (
	"reflect"
	"testing"
)

// TestWrapPath: a long path breaks after separators, never inside a name
// that fits on a line, and a name longer than a line is cut.
func TestWrapPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		cols int
		want []string
	}{
		{"~/src/web-app", 40, []string{"~/src/web-app"}},
		{"~/src/web-app/.worktrees/fix-checkout-race", 20, []string{"~/src/web-app/", ".worktrees/", "fix-checkout-race"}},
		{"/tmp/aaaaaaaaaaaa/b", 8, []string{"/tmp/", "aaaaaaaa", "aaaa/b"}},
	} {
		if got := wrapPath(tc.path, tc.cols); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("wrapPath(%q, %d) = %q, want %q", tc.path, tc.cols, got, tc.want)
		}
	}
}

// TestReviewBlank: the review says it is reading until the diff loads,
// shows an empty state for no changes, and the error when it failed.
func TestReviewBlank(t *testing.T) {
	for _, tc := range []struct {
		err    string
		loaded bool
		files  int
		state  int
		line   string
	}{
		{"", false, 0, reviewReading, "Reading the diff…"},
		{"", true, 0, reviewEmpty, "No changes from main."},
		{"git failed", true, 0, reviewFailed, "git failed"},
		{"", true, 2, reviewFiles, ""},
	} {
		if state, line := reviewBlank(tc.err, tc.loaded, tc.files, "refs/heads/main"); state != tc.state || line != tc.line {
			t.Errorf("%+v: %d %q", tc, state, line)
		}
	}
}
