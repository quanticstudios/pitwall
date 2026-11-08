package daemon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
)

func TestLimitResumeFreezePreservesPending(t *testing.T) {
	for _, event := range []string{"SessionEnd", "activity removal", "cancel", "timer"} {
		t.Run(event, func(t *testing.T) {
			first, f, clock, path := savedLimit(t, model.ProviderClaude)
			first.Freeze()
			switch event {
			case "SessionEnd":
				claudeHook(t, first, "a", `{"session_id":"agent-session","hook_event_name":"SessionEnd"}`)
			case "activity removal":
				first.mu.Lock()
				first.dropActivity("a")
				first.mu.Unlock()
			case "cancel":
				if err := first.cancelResume(proto.CancelResume{Pane: "a"}); err != nil {
					t.Fatal(err)
				}
			case "timer":
				clock.fire()
				if got := f.pane(0).got(); got != "" {
					t.Fatalf("typed while frozen: %q", got)
				}
			}
			first.Save()
			first.shutdown()
			saved, err := store.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.Activities) != 1 || saved.Activities[0].State != model.StateError || !saved.Panes[0].LimitResume.Pending {
				t.Fatalf("frozen save lost the stopped turn: activities %+v, resume %+v", saved.Activities, saved.Panes[0].LimitResume)
			}
			d := loadLimit(t, f, path, nil)
			readyLimit(d, "a")
			clock.fire()
			if got := gotAfter(t, f.pane(2), 0, 9); got != "continue\r" {
				t.Fatalf("after frozen restart: %q", got)
			}
		})
	}
}

func TestLimitResumeFreezeThaw(t *testing.T) {
	d, f, clock, _ := savedLimit(t, model.ProviderClaude)
	d.Freeze()
	clock.fire()
	if resumes(d, "a").IsZero() {
		t.Fatal("frozen timer consumed the resume")
	}
	d.Thaw()
	clock.fire()
	if got := gotAfter(t, f.pane(0), 0, 9); got != "continue\r" {
		t.Fatalf("after thaw: %q", got)
	}
}

func TestLimitResumeInterruptedContinuationGetsRestartNudge(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderClaude)
	clock.fire()
	if got := gotAfter(t, f.pane(0), 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
	first.shutdown()
	d := loadLimit(t, f, path, nil)
	d.mu.Lock()
	q := d.mail.queue["a"]
	if len(q) != 1 || q[0].text != restartLine {
		d.mu.Unlock()
		t.Fatalf("interrupted continuation has no restart nudge: %+v", q)
	}
	if i := d.activityIndex("a"); i >= 0 && d.st.Activities[i].State == model.StateError {
		d.mu.Unlock()
		t.Fatal("interrupted continuation shows the expired limit error")
	}
	d.mu.Unlock()
	readyLimit(d, "a")
	d.deliver("a")
	if got := gotAfter(t, f.pane(2), 0, len(restartLine)+1); got != restartLine+"\r" {
		t.Fatalf("restart nudge: %q", got)
	}
	if !resumes(d, "a").IsZero() {
		t.Fatal("consumed usage limit armed again")
	}
}

func TestLimitResumeReadinessTimeout(t *testing.T) {
	for _, prov := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(prov), func(t *testing.T) {
			first, f, clock, path := savedLimit(t, prov)
			first.shutdown()
			clock.now = clock.now.Add(2 * time.Hour)
			d := loadLimit(t, f, path, nil)
			clock.fire()
			if resumes(d, "a").IsZero() {
				t.Fatal("resume did not wait for readiness")
			}
			clock.now = clock.now.Add(2 * time.Minute)
			before := len(clock.fns)
			clock.fire()
			if !resumes(d, "a").IsZero() || len(clock.fns) != before {
				t.Fatal("unready agent kept polling after two minutes")
			}
			a := d.activityOf("a")
			if a.State != model.StateError || !strings.Contains(a.Detail, "did not become ready") || !strings.Contains(a.Detail, "continue manually") {
				t.Fatalf("timeout is not visible: %+v", a)
			}
			readyLimit(d, "a")
			clock.fire()
			if got := f.pane(2).got(); got != "" {
				t.Fatalf("typed after timeout: %q", got)
			}
			saved, err := store.Load(path)
			if err != nil || saved.Panes[0].LimitResume.Pending {
				t.Fatalf("timeout not saved: %+v, %v", saved.Panes, err)
			}
			again := loadLimit(t, f, path, nil)
			setScreen(f.pane(4), []string{"You've hit your session limit · resets 3pm (UTC)"})
			again.checkLimits()
			if !resumes(again, "a").IsZero() || len(clock.fns) != before {
				t.Fatal("timed out resume rearmed after restart")
			}
		})
	}
}

func TestChatLimitResumeSaveFailureStillSends(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, prov := chatDaemon(t, func(o *Options) {
		o.ResumeAfterLimit = func() bool { return true }
		o.Save = func(model.State) error { return errors.New("disk is full") }
	})
	r := chatDo(t, d, proto.ChatNew{Dir: t.TempDir(), Prompt: "fix it"})
	id, drv := r.Chat, prov.driver(t)
	drv.event(session.Event{Type: session.EventLimit, Limit: &session.Limit{Rejected: true, Resets: clock.now.Add(time.Hour)}})
	drv.end(session.TurnFailed)
	waitUntil(t, "chat resume set", func() bool { return !resumes(d, id).IsZero() })
	clock.fire()
	got := drv.got()
	if len(got) == 0 || got[len(got)-1] != "send continue" {
		t.Fatalf("save failure blocked the chat continuation: %q", got)
	}
}
