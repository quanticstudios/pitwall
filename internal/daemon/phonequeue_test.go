package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// The phone page lists a pane chat's queue and why it waits, and its
// Remove and Send now act on a row.
func TestPhoneQueue(t *testing.T) {
	d, f, do := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.st.Activities[d.activityIndex("a")].State = model.StateAwaitingInput
	d.mu.Unlock()
	ids := enqueue(d, "token=abc123 first", "second", "third")
	if st, body := do("POST", "/api/queue", `{"pane":"a","id":"`+ids[2]+`","action":"move","index":0}`); st != 200 {
		t.Fatalf("move: %d %s", st, body)
	}
	d.mu.Lock()
	moved, _, _ := d.chatQueued("a")
	d.mu.Unlock()
	if len(moved) != 3 || moved[0].ID != ids[2] || moved[1].ID != ids[0] {
		t.Fatalf("queue after move: %+v", moved)
	}
	if st, body := do("POST", "/api/queue", `{"pane":"a","id":"`+ids[2]+`","action":"remove"}`); st != 200 {
		t.Fatalf("remove the moved: %d %s", st, body)
	}
	d.chatReason("a")
	var tab remote.Tab
	for _, tb := range d.phoneTabs() {
		if tb.Pane == "a" {
			tab = tb
		}
	}
	if len(tab.Queue) != 2 || tab.Queue[0].ID != ids[0] || strings.Contains(tab.Queue[0].Text, "abc123") || tab.QueueHold != waitAnswer {
		t.Fatalf("phone tab queue %+v hold %q", tab.Queue, tab.QueueHold)
	}
	if st, body := do("POST", "/api/queue", `{"pane":"a","id":"`+ids[0]+`","action":"remove"}`); st != 200 {
		t.Fatalf("remove: %d %s", st, body)
	}
	if st, _ := do("POST", "/api/queue", `{"pane":"a","id":"`+ids[1]+`","action":"edit"}`); st != 400 {
		t.Fatalf("edit from the phone: %d, want 400", st)
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"> "}
	f.pane(0).mu.Unlock()
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	before := len(f.pane(0).got())
	if st, body := do("POST", "/api/queue", `{"pane":"a","id":"`+ids[1]+`","action":"now"}`); st != 200 {
		t.Fatalf("send now: %d %s", st, body)
	}
	if got := gotAfter(t, f.pane(0), before, len("second")); !strings.Contains(got, "second") {
		t.Fatalf("send now typed %q", got)
	}
	d.mu.Lock()
	rows, _, _ := d.chatQueued("a")
	d.mu.Unlock()
	if len(rows) != 0 {
		t.Fatalf("queue after both: %+v", rows)
	}
}
