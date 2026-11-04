package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

type fakeAwakeLock struct{ held, takes, releases int }

func (f *fakeAwakeLock) inhibit(context.Context) (io.Closer, error) {
	f.held++
	f.takes++
	return f, nil
}

func (f *fakeAwakeLock) Close() error {
	f.held--
	f.releases++
	return nil
}

func TestKeepAwake(t *testing.T) {
	now := time.Unix(1000, 0)
	var a awakeLock
	f := &fakeAwakeLock{}
	step := func(on, working bool, advance, grace time.Duration, held int) {
		t.Helper()
		now = now.Add(advance)
		if err := a.update(context.Background(), on, working, now, grace, f.inhibit); err != nil {
			t.Fatal(err)
		}
		if f.held != held {
			t.Fatalf("on=%v working=%v at %s: held %d, want %d", on, working, now, f.held, held)
		}
	}
	grace := config.DefaultKeepAwakeGrace
	step(false, true, 0, grace, 0)
	step(true, false, 0, grace, 0) // enabling while idle takes no lock
	step(true, true, 0, grace, 1)
	step(true, true, time.Hour, grace, 1)
	if f.takes != 1 {
		t.Fatal("working repeatedly took locks")
	}
	step(true, false, 0, grace, 1)
	step(true, false, grace-time.Second, grace, 1)
	step(true, false, time.Second, grace, 0)
	step(true, true, 0, grace, 1)
	step(true, false, 0, grace, 1)
	step(true, true, time.Minute, grace, 1) // work cancels the idle deadline
	step(true, false, 0, grace, 1)
	step(true, false, time.Minute, 30*time.Second, 0) // grace applies live
	step(true, true, 0, grace, 1)
	step(false, true, 0, grace, 0) // off releases even during work
	step(true, false, 0, grace, 0) // off discards the old grace
	step(true, true, 0, grace, 1)
	step(true, false, 0, 0, 0)
	step(true, true, 0, grace, 1)
	a.close() // the old daemon exits during a handoff
	if f.held != 0 || f.releases != f.takes {
		t.Fatal("daemon exit left a lock")
	}
	a = awakeLock{} // the new daemon re-evaluates the handed-off activity
	step(true, true, 0, grace, 1)
	a.close()
	a = awakeLock{}
	step(true, false, 0, grace, 0) // a handoff does not inherit idle grace
}

func TestKeepAwakeFailureOnce(t *testing.T) {
	var a awakeLock
	calls, failures := 0, 0
	inhibit := func(context.Context) (io.Closer, error) {
		calls++
		return nil, errors.New("no system bus")
	}
	for i := range 10 {
		if err := a.update(context.Background(), i != 3, true, time.Unix(int64(i), 0), time.Minute, inhibit); err != nil {
			failures++
		}
	}
	if calls != 1 || failures != 1 {
		t.Fatalf("%d attempts, %d errors; want one each", calls, failures)
	}
	a.close()
}

type failedAwakeLock struct{ fakeAwakeLock }

func (*failedAwakeLock) Err() error { return errors.New("inhibitor exited") }

func TestKeepAwakeLostLock(t *testing.T) {
	f := &failedAwakeLock{fakeAwakeLock{held: 1, takes: 1}}
	a := awakeLock{lock: f}
	for i := range 2 {
		err := a.update(context.Background(), true, true, time.Unix(int64(i), 0), time.Minute, f.inhibit)
		if (err != nil) != (i == 0) {
			t.Fatalf("update %d: error %v", i, err)
		}
	}
	if f.held != 0 || f.takes != 1 || f.releases != 1 {
		t.Fatalf("lost lock: %+v", f)
	}
}

func TestKeepAwakeActivity(t *testing.T) {
	for _, tc := range []struct {
		state model.AgentState
		want  bool
	}{
		{model.StateWorking, true},
		{model.StatePendingApproval, false},
		{model.StatePlanReady, false},
		{model.StateAwaitingInput, false},
		{model.StateCompleted, false},
		{"", false},
	} {
		d := &Daemon{st: model.State{Activities: []model.Activity{{Provider: model.ProviderCodex, State: tc.state}}}}
		if got := d.agentsWorking(); got != tc.want {
			t.Errorf("%s: working %v", tc.state, got)
		}
	}
	d := &Daemon{st: model.State{Activities: []model.Activity{{Provider: model.ProviderTerminal, State: model.StateWorking}}}}
	if d.agentsWorking() {
		t.Fatal("a terminal program counts as an agent")
	}
	for _, state := range []string{session.StateRunning, session.StateWaiting, session.StateReady} {
		snap := session.Snapshot{SessionState: state, Turns: []session.TurnSummary{{}}}
		if state == session.StateWaiting {
			snap.PendingApprovals = []session.Approval{{}}
		}
		a, _, _ := chatState(snap, false)
		d := &Daemon{chats: map[string]*chat{"c": {}}, st: model.State{Activities: []model.Activity{{PaneID: "c", Provider: "acp:test", State: a}}}}
		if got := d.agentsWorking(); got != (state == session.StateRunning) {
			t.Errorf("chat %s: working %v", state, got)
		}
	}
}

type awakeCloser func() error

func (f awakeCloser) Close() error { return f() }

func TestServeKeepAwake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(on bool) {
		t.Helper()
		body := "[agents]\nkeep_awake_grace = \"0s\"\nkeep_awake = false\n"
		if on {
			body = "[agents]\nkeep_awake_grace = \"0s\"\nkeep_awake = true\n"
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(true)
	events := make(chan string, 10)
	d, _, stop := runDaemon(t, &fakes{}, func(o *Options) {
		o.KeepAwake = func() (bool, time.Duration) {
			s, _ := config.LoadFile(path)
			return s.KeepAwake, s.KeepAwakeGrace
		}
		o.InhibitIdle = func(context.Context) (io.Closer, error) {
			events <- "take"
			return awakeCloser(func() error { events <- "release"; return nil }), nil
		}
	})
	defer stop()
	want := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event %s, want %s", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no %s event", want)
		}
	}
	set := func(state model.AgentState) {
		d.mu.Lock()
		d.st.Activities = []model.Activity{{Provider: model.ProviderCodex, State: state}}
		d.changed()
		d.mu.Unlock()
	}
	set(model.StateWorking)
	want("take")
	write(false) // config alone wakes the loop within a second
	want("release")
	write(true)
	want("take")
	set(model.StatePendingApproval)
	want("release")
	set(model.StateWorking)
	want("take")
	d.mu.Lock()
	d.stop()
	d.mu.Unlock()
	want("release")
}
