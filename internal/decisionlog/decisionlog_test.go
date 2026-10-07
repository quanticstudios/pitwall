package decisionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRotate: a line that would take the file past the cap moves it to
// .1, replacing the old one, and Read returns both files oldest first.
func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "decisions.jsonl")
	l, err := open(path, 300)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range 10 {
		l.Add(Event{T: start.Add(time.Duration(i) * time.Second), Kind: Call, ID: fmt.Sprint("id", i), Feature: "approvals", Ms: 100})
	}
	l.Close(time.Second)
	for _, p := range []string{path, path + ".1"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 300 {
			t.Errorf("%s is %d bytes", p, fi.Size())
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", p, fi.Mode())
		}
	}
	evs, err := Read(path, time.Time{})
	if err != nil || len(evs) < 2 || evs[len(evs)-1].ID != "id9" || evs[0].V != Version {
		t.Fatalf("read %+v %v", evs, err)
	}
	for i := 1; i < len(evs); i++ {
		if !evs[i].T.After(evs[i-1].T) {
			t.Errorf("out of order: %+v", evs)
		}
	}
	if since, _ := Read(path, start.Add(9*time.Second)); len(since) != 1 {
		t.Errorf("since: %+v", since)
	}
}

// TestReport checks the report's numbers on a fixture: two holdout arms
// of 30 answered approvals, turn checks, triage and a failed call.
func TestReport(t *testing.T) {
	to := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	at := to.Add(-48 * time.Hour)
	var evs []Event
	n := 0
	add := func(c, o Event) {
		n++
		id := fmt.Sprint("d", n)
		c.Kind, c.ID, c.T = Call, id, at
		evs = append(evs, c)
		if o.User != "" {
			o.Kind, o.ID, o.Feature, o.T = Outcome, id, c.Feature, at
			evs = append(evs, o)
		}
	}
	for i := range 30 { // shown: waits 1..30s; Jev allows all; 3 denied
		user := Allowed
		if i < 3 {
			user = Denied
		}
		add(Event{Feature: approvals, Ms: 200, Tokens: 1000, Answer: "allow", Conf: 0.95},
			Event{User: user, WaitMs: int64(i+1) * 1000})
	}
	for i := range 30 { // held out: waits 11..40s; 5 denies agreed, 2 asks allowed
		c := Event{Feature: approvals, Ms: 400, Tokens: 1000, Answer: "allow", Conf: 0.95, Held: true}
		user := Allowed
		switch {
		case i < 5:
			c.Answer, user = "deny", Denied
		case i < 7:
			c.Answer, c.Conf = "ask", 0.6
		}
		add(c, Event{User: user, WaitMs: int64(i+11) * 1000, Held: true})
	}
	add(Event{Feature: approvals, Err: "timeout", Tokens: 500, Estimated: true}, Event{User: Unknown})
	add(Event{Feature: turnCheck, Ms: 100, Tokens: 700, Answer: "check"}, Event{User: Prompted, WaitMs: 60_000})
	add(Event{Feature: turnCheck, Ms: 100, Tokens: 700, Answer: "check"}, Event{User: Prompted, WaitMs: 600_000})
	add(Event{Feature: turnCheck, Ms: 100, Tokens: 700, Answer: "done"}, Event{})
	add(Event{Feature: turnCheck, Ms: 100, Tokens: 700, Answer: "done"}, Event{})
	add(Event{Feature: triage, Ms: 100, Tokens: 800, Answer: "now"}, Event{User: Focused, WaitMs: 3000})
	add(Event{Feature: triage, Ms: 100, Tokens: 800, Answer: "fyi"}, Event{})

	var b strings.Builder
	Report(&b, evs, to.Add(-7*24*time.Hour), to)
	out := b.String()
	t.Log("\n" + out) // go test -v shows a sample
	for _, want := range []string{
		"67 calls, 64 outcomes",
		"  approvals        61      2%   200 ms   400 ms",
		"failures: timeout 1",
		"about $0.0026 for 64900 input tokens at $0.04 per million",
		"1% of the tokens are guessed",
		"suggestion shown:  median 15.0s, p75 23.0s (n=30)",
		"held out:          median 25.0s, p75 33.0s (n=30)",
		"left out, no event showed the answer: 1",
		"Jev agreed with you on 92% (n=60); held out only 93% (n=30); at confidence 0.9 or more 95% (n=58)",
		"Jev said ask or deny and you allowed: 2; Jev said allow and you denied: 3",
		"of the 8 you denied, Jev said ask or deny on 5",
		"marked Check: 1 of 2 (50%); marked Done: 0 of 2 (0%); lift +50 points",
		"now    median 3.0s (n=1)",
		"not focused before the pane moved on: 1",
		"Verdict: you answer faster with the suggestion shown (median 15.0s shown vs 25.0s held out",
		"About $0.0026 over 7 days.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}

	// Under MinPerArm in an arm, no verdict.
	b.Reset()
	Report(&b, evs[:20], to.Add(-7*24*time.Hour), to)
	if !strings.Contains(b.String(), "not enough data yet. It needs 30 answered approvals in each arm; there are 10 shown and 0 held out") {
		t.Errorf("small report:\n%s", b.String())
	}
}
