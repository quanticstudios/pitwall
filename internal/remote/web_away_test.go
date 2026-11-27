//go:build unix

package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestWebAway runs the phone page's Since you left card in headless
// Chromium (testdata/away_drive.js): a marker in the log above the first
// missed entry always, and the card above the box only when what was
// missed is taller than the log's view; the card shows how long, the
// summary and the facts, Dismiss clears both and POSTs /api/away, and so
// does scrolling up to the marker and back down; a card whose summary is
// on its way says so. The page beats POST /api/view while it shows the
// chat and says when it goes to the background. With PITWALL_AWAY_SHOTS set it writes
// the card as a PNG there.
func TestWebAway(t *testing.T) {
	p := newWebPage(t, "testdata/away_drive.js")
	p.mu.Lock()
	p.views = true
	p.mu.Unlock()
	var got struct {
		Text, TallMark, ShortMark  string
		ShortCard                  int
		Dismissed, Asking          bool
		ReadCardStays, ReadThrough bool
		Error                      string
	}
	json.Unmarshal([]byte(p.drive(t, "#pane=c")), &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	for _, want := range []string{"Since you left · 2h 14m · Done", "TestLoginRefresh still fails", "Pick: pad the expiry", "1 message · 2 files edited · 2 commands · tests failed", "Last failed: go test ./auth/..."} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("card %q lacks %q", got.Text, want)
		}
	}
	if !got.Dismissed || !got.Asking {
		t.Errorf("dismissed %v, asking %v", got.Dismissed, got.Asking)
	}
	// The marker sits in the log either way; the card only when what was
	// missed is taller than the log's view.
	if got.TallMark != "Since you left · 2h 14m" || got.ShortMark != "Since you left · 2h 15m" || got.ShortCard != 0 {
		t.Errorf("tall marker %q, short marker %q, short card %d", got.TallMark, got.ShortMark, got.ShortCard)
	}
	if !got.ReadCardStays || !got.ReadThrough {
		t.Errorf("scrolled up to the marker: card stays %v; back at the bottom: gone %v", got.ReadCardStays, got.ReadThrough)
	}
	// Opening the chat beats that the page shows it; reading through and
	// the x dismiss; hiding the page and showing it again say so.
	if want := []string{`/api/view {"pane":"c","on":true}`, `/api/away {"pane":"c"}`, `/api/away {"pane":"c"}`, `/api/view {"pane":"c","on":false}`, `/api/view {"pane":"c","on":true}`}; !reflect.DeepEqual(p.calls, want) {
		t.Errorf("calls %q, want %q", p.calls, want)
	}

	dir := os.Getenv("PITWALL_AWAY_SHOTS")
	if dir == "" {
		return
	}
	file := filepath.Join(dir, "web-away.png")
	p.chromium(t, func(exited chan error) bool {
		select {
		case err := <-exited:
			exited <- err
			return err == nil
		case <-time.After(30 * time.Second):
			return false
		}
	}, "--screenshot="+file, "--window-size=400,760", "--force-device-scale-factor=2", "--hide-scrollbars", p.url+"#pane=c&shot=1")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}
