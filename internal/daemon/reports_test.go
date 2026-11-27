package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestParseReport(t *testing.T) {
	for _, c := range []struct {
		name, msg  string
		status     model.ReportStatus
		line, body string
	}{
		{"done", "Fixed 4 gaps.\nSuite green.\n\nDONE", model.ReportDone, "Fixed 4 gaps.", "Fixed 4 gaps.\nSuite green."},
		{"blocked", "Built the parser.\nBLOCKED: needs the card's placement", model.ReportBlocked, "needs the card's placement", "Built the parser."},
		{"blocked bare", "Tried twice.\nBLOCKED", model.ReportBlocked, "Tried twice.", "Tried twice."},
		{"neither", "Still running the suite.", model.ReportProgress, "Still running the suite.", "Still running the suite."},
		{"trailing whitespace", "## Status\n**Shipped** the fix.\n  DONE  \n\n \t", model.ReportDone, "Status", "## Status\n**Shipped** the fix."},
		{"emphasis", "All green.\n**DONE**", model.ReportDone, "All green.", "All green."},
		{"quotes DONE mid-text", "The last line must say DONE.\nI have not checked CI yet.", model.ReportProgress, "The last line must say DONE.", "The last line must say DONE.\nI have not checked CI yet."},
		{"DONE inside a line", "Tests: DONE", model.ReportProgress, "Tests: DONE", "Tests: DONE"},
		{"not blocked", "BLOCKEDNESS is a word", model.ReportProgress, "BLOCKEDNESS is a word", "BLOCKEDNESS is a word"},
	} {
		s, line, body := parseReport(c.msg)
		if s != c.status || line != c.line || body != c.body {
			t.Errorf("%s: got %q %q %q, want %q %q %q", c.name, s, line, body, c.status, c.line, c.body)
		}
	}
}

// stop ends a turn of pane's Claude with reply as its last message.
func stop(t *testing.T, d *Daemon, pane, reply string) {
	t.Helper()
	msg, _ := json.Marshal(reply)
	claudeHook(t, d, pane, `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	claudeHook(t, d, pane, `{"hook_event_name":"Stop","last_assistant_message":`+string(msg)+`}`)
}

func TestReportCards(t *testing.T) {
	s := fleetSettings()
	d, _ := fleetDaemon(t, time.Now(), &s)
	stop(t, d, "wa", "Opened PR #566 with the fix.\nDONE")
	got := snapshot(d).Reports["pa"]
	if len(got) != 1 {
		t.Fatalf("cards %+v, want one in the orchestrator's chat", snapshot(d).Reports)
	}
	if r := got[0]; r.Tab != "w" || r.Name != "fix-login" || r.PR != 566 || r.Status != model.ReportDone || r.Line != "Opened PR #566 with the fix." {
		t.Fatalf("card %+v", r)
	}
	// The same turn's Stop again (a hook sent twice) posts nothing new.
	d.mu.Lock()
	d.noteReport(d.st.Panes[1], "Opened PR #566 with the fix.\nDONE", time.Now())
	d.mu.Unlock()
	if n := len(snapshot(d).Reports["pa"]); n != 1 {
		t.Fatalf("%d cards after a repeated turn, want 1", n)
	}
	stop(t, d, "wa", "Rebased.\nBLOCKED: CI is red on main")
	stop(t, d, "wa", "Halfway there.")
	got = snapshot(d).Reports["pa"]
	if len(got) != 3 || got[1].Status != model.ReportBlocked || got[1].Line != "CI is red on main" || got[2].Status != model.ReportProgress {
		t.Fatalf("cards %+v, want Done, Blocked, In progress", got)
	}
	// The orchestrator's own turns post nothing.
	stop(t, d, "pa", "Merged it.\nDONE")
	if n := len(snapshot(d).Reports); n != 1 {
		t.Fatalf("%d chats have cards, want only the orchestrator's", n)
	}
}

func TestReportCardsCap(t *testing.T) {
	s := fleetSettings()
	d, _ := fleetDaemon(t, time.Now(), &s)
	for range maxReports + 5 {
		stop(t, d, "wa", "Step.")
	}
	got := snapshot(d).Reports["pa"]
	if len(got) != maxReports || got[len(got)-1].Turn != maxReports+5 {
		t.Fatalf("%d cards, last turn %d", len(got), got[len(got)-1].Turn)
	}
}
