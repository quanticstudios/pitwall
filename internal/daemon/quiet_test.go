package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// quietSince is pane's Activity.QuietSince as clients see it.
func quietSince(d *Daemon, pane string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.snapshot().Activities {
		if a.PaneID == pane {
			return a.QuietSince
		}
	}
	return time.Time{}
}

// age moves pane's last screen change back by ago.
func age(d *Daemon, pane string, ago time.Duration) {
	d.mu.Lock()
	d.quiet[pane].since = d.quiet[pane].since.Add(-ago)
	d.mu.Unlock()
}

// A working agent whose screen stays still past quiet_after gets
// QuietSince; a change on screen clears it, and so does the turn ending.
func TestQuiet(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return 20 * time.Minute }
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v at the first look", got)
	}
	age(d, "a", 21*time.Minute)
	d.checkQuiet()
	if got := quietSince(d, "a"); got.IsZero() || time.Since(got) < 20*time.Minute {
		t.Fatalf("QuietSince = %v after 21 still minutes", got)
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"new output"}
	f.pane(0).mu.Unlock()
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v after the screen changed", got)
	}
	age(d, "a", 21*time.Minute)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v after the turn ended", got)
	}
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q with continue_when_quiet off", got)
	}
}

// With continue_when_quiet, a quiet agent gets continue typed once per
// stretch of work, never over a form, and again only after the user typed.
func TestContinueWhenQuiet(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return 20 * time.Minute }
	d.o.ContinueWhenQuiet = func() bool { return true }
	p := f.pane(0)
	quiet := func() {
		t.Helper()
		d.checkQuiet()
		age(d, "a", 21*time.Minute)
		d.checkQuiet()
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)

	p.mu.Lock()
	p.screen = []string{"Do you want to proceed?", "1. Yes"}
	p.mu.Unlock()
	quiet()
	time.Sleep(50 * time.Millisecond) // typing reaches the pane asynchronously
	if got := p.got(); got != "" {
		t.Fatalf("typed %q over a form", got)
	}

	d.mu.Lock()
	delete(d.quiet, "a") // a new stretch
	d.mu.Unlock()
	p.mu.Lock()
	p.screen = []string{"> "}
	p.mu.Unlock()
	quiet()
	if got := gotAfter(t, p, 0, 9); got != "continue\r" {
		t.Fatalf("typed %q, want continue", got)
	}
	quiet()
	time.Sleep(50 * time.Millisecond)
	if got := p.got(); got != "continue\r" {
		t.Fatalf("typed %q, want one continue", got)
	}
	d.noteUserKey("a", []byte("y"))
	quiet()
	if got := gotAfter(t, p, 9, 9); !strings.HasSuffix(got, "continue\r") {
		t.Fatalf("typed %q after the user's key, want a second continue", got)
	}
}

// pitwall status sets a tab's line on one line, cut to statusRunes, and
// an empty text clears it.
func TestSetStatus(t *testing.T) {
	d, _ := talkDaemon(t)
	status := func() string {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.workspace("w").Status
	}
	if err := d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w", Text: "CI\n 3/7 "}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "CI 3/7" {
		t.Fatalf("status %q", got)
	}
	d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w", Text: strings.Repeat("x", 200)})
	if got := []rune(status()); len(got) != statusRunes || got[statusRunes-1] != '…' {
		t.Fatalf("long status %q", string(got))
	}
	d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w"})
	if got := status(); got != "" {
		t.Fatalf("cleared status %q", got)
	}
	if err := d.handle(t.Context(), proto.SetStatus{WorkspaceID: "nope", Text: "x"}); err == nil {
		t.Fatal("a missing tab took a status")
	}
}
