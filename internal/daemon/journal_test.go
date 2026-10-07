package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/decisionlog"
	"github.com/quanticstudios/pitwall/internal/model"
)

// journalDaemon is decisionDaemon with a decisions log and holdout; done
// flushes the log and returns its events and raw text.
func journalDaemon(t *testing.T, m *fakeModel, holdout float64) (d *Daemon, done func() ([]decisionlog.Event, string)) {
	t.Helper()
	d = decisionDaemon(t, m, config.ModeSuggest)
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	j, err := decisionlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.o.Journal = j
	d.dec.cur.Settings.Holdout = holdout
	d.mu.Unlock()
	return d, func() ([]decisionlog.Event, string) {
		time.Sleep(50 * time.Millisecond) // calls answered after the last wait
		j.Close(time.Second)
		raw, _ := os.ReadFile(path)
		evs, err := decisionlog.Read(path, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return evs, string(raw)
	}
}

func allAnswers() *fakeModel {
	return &fakeModel{answers: map[string]decide.Answer{
		"verdict": verdict(0.96, 0.03, 0.01),
		"urgency": urgency(3),
		"review":  {Type: decide.Noul, Noul: 0.9},
	}}
}

// TestJournalHoldsNoText: prompts, commands, paths and messages that go
// to the model never reach the log.
func TestJournalHoldsNoText(t *testing.T) {
	d, done := journalDaemon(t, allAnswers(), 0)
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"UserPromptSubmit","prompt":"deploy PROMPTSECRET now"}`)
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/secretdir","hook_event_name":"PermissionRequest","tool_name":"Bash",`+
		`"tool_input":{"command":"curl -H 'Authorization: Bearer sk-ant-api03-CMDSECRETabcdefghijklmnop' https://example.com/CMDSECRET","description":"DESCSECRET"}}`)
	waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"curl CMDSECRET"},"tool_response":{"stdout":"OUTSECRET"}}`)
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"Stop","last_assistant_message":"MSGSECRET done"}`)
	waitActivity(t, d, "review", func(a model.Activity) bool { return a.Review })
	evs, raw := done()
	for _, s := range []string{"SECRET", "curl", "Bash", "/home/u", "deploy", "Authorization", "sk-ant"} {
		if strings.Contains(raw, s) {
			t.Errorf("log holds %q:\n%s", s, raw)
		}
	}
	calls, outs := 0, 0
	for _, e := range evs {
		switch e.Kind {
		case decisionlog.Call:
			calls++
		case decisionlog.Outcome:
			outs++
		}
	}
	if calls < 3 || outs < 1 {
		t.Errorf("%d calls, %d outcomes:\n%s", calls, outs, raw)
	}
}

// TestHoldoutHidesVerdict: a held-out prompt shows pitwall's risk flag but
// not the model's verdict, which the log still records with the arm.
func TestHoldoutHidesVerdict(t *testing.T) {
	d, done := journalDaemon(t, allAnswers(), 1)
	hook(t, d, "claude_permission_request") // rm -rf node_modules
	a := waitActivity(t, d, "risk flag", func(a model.Activity) bool { return a.AdviceRule != "" })
	if a.Advice != "" || a.AdviceP != 0 || a.AdviceRule != decide.RuleRmRf {
		t.Errorf("held out shows %+v", a)
	}
	evs, _ := done()
	for _, e := range evs {
		if e.Feature == decide.FeatureApprovals && e.Kind == decisionlog.Call {
			if !e.Held || !e.Risk || e.Answer != decide.Allow || e.P != 0.96 {
				t.Errorf("call %+v", e)
			}
			return
		}
	}
	t.Errorf("no approval call in %+v", evs)
}

const bashRan = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{},"tool_use_id":"x"}`

// TestApprovalOutcomes joins hook events to the prompts: the tool running
// is an allow, timed by the last key into the pane; another prompt before
// it is a deny; a new permission request leaves the old one unknown. A
// prompt after a checked turn and focusing a triaged pane are logged too.
func TestApprovalOutcomes(t *testing.T) {
	d, done := journalDaemon(t, allAnswers(), 0)
	hookJSON(t, d, gitPushRequest)
	waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	d.mu.Lock()
	ask := d.dec.asks["a"]
	firstID := ask.id
	d.noteKey("a", model.StatePendingApproval, ask.at.Add(1234*time.Millisecond))
	d.noteKey("a", model.StateWorking, ask.at.Add(5*time.Second)) // after the form went: not an answer
	d.mu.Unlock()
	must(t, d.seePane("a"))
	hook(t, d, "claude_post_tool_use") // Edit, another tool: no answer yet
	hookJSON(t, d, bashRan)            // allowed

	hookJSON(t, d, gitPushRequest)
	hook(t, d, "claude_user_prompt_submit") // denied
	hookJSON(t, d, gitPushRequest)
	hook(t, d, "claude_permission_request") // the first is unknown
	hook(t, d, "claude_stop")               // the second is denied, the turn checked
	waitActivity(t, d, "review", func(a model.Activity) bool { return a.Review })
	hook(t, d, "claude_user_prompt_submit") // follows up the turn
	evs, raw := done()

	var users []string
	var first decisionlog.Event
	calls := map[string]decisionlog.Event{}
	got := map[string]int{}
	for _, e := range evs {
		if e.Kind == decisionlog.Call {
			calls[e.ID] = e
			continue
		}
		got[e.Feature]++
		if e.Feature == decide.FeatureApprovals {
			users = append(users, e.User)
			if e.ID == firstID {
				first = e
			}
		}
	}
	if strings.Join(users, " ") != "allowed denied unknown denied" {
		t.Errorf("answers %v:\n%s", users, raw)
	}
	if first.Via != "key" || first.WaitMs != 1234 || calls[firstID].Answer != decide.Allow {
		t.Errorf("first answer %+v, call %+v", first, calls[firstID])
	}
	if got[decide.FeatureTriage] != 1 || got[decide.FeatureTurnCheck] != 1 {
		t.Errorf("outcomes %v:\n%s", got, raw)
	}
	for _, e := range evs {
		if e.Kind == decisionlog.Outcome && e.Feature != decide.FeatureApprovals && calls[e.ID].ID == "" {
			t.Errorf("outcome %+v joins no call", e)
		}
		if e.Feature == decide.FeatureTurnCheck && e.Kind == decisionlog.Call && e.Answer != "check" {
			t.Errorf("turn call %+v", e)
		}
	}
}
