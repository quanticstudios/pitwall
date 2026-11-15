package control

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestWait(t *testing.T) {
	claude, shell := model.ProviderClaude, model.Provider("")
	gone := driveState(claude, model.StateWorking, false, 0)
	gone.Panes = nil
	unknown := driveState(shell, "", true, 0)
	unknown.Panes[0].ExitUnknown = true
	type exit struct {
		code int
		ok   bool
	}
	for _, tc := range []struct {
		name   string
		until  string
		states []model.State
		exit   exit // what the exit lookup finds
		want   Outcome
	}{
		{"working then idle", "done", []model.State{driveState(claude, model.StateWorking, false, 0), driveState(claude, "", false, 0)}, exit{}, Outcome{Reason: Reached, State: "idle"}},
		{"idle from the start", "done", []model.State{driveState(claude, "", false, 0)}, exit{}, Outcome{}},
		{"done", "done", []model.State{driveState(claude, model.StateCompleted, false, 0)}, exit{}, Outcome{Reason: Reached, State: "done"}},
		{"blocked", "done", []model.State{driveState(claude, model.StatePendingApproval, false, 0)}, exit{}, Outcome{Reason: Blocked, Question: "Run make?"}},
		{"until blocked", "blocked", []model.State{driveState(claude, model.StateAwaitingInput, false, 0)}, exit{}, Outcome{Reason: Reached, State: "blocked"}},
		{"parked until done", "done", []model.State{driveState(claude, model.StateParked, false, 0)}, exit{}, Outcome{Reason: Reached, State: "parked"}},
		{"parked until idle", "idle", []model.State{driveState(claude, model.StateParked, false, 0)}, exit{}, Outcome{Reason: Reached, State: "parked"}},
		{"pr-open until idle", "idle", []model.State{driveState(claude, model.StateWorking, false, 0), driveState(claude, model.StatePROpen, false, 0)}, exit{}, Outcome{Reason: Reached, State: "pr-open"}},
		{"pr-open until done", "done", []model.State{driveState(claude, model.StatePROpen, false, 0)}, exit{}, Outcome{}},
		// The agent is gone with its pane: it ran an agent, so its exit is
		// not what until done waited for.
		{"agent gone until done", "done", []model.State{driveState(claude, model.StateWorking, false, 0), gone}, exit{0, true}, Outcome{Reason: Exited, Exit: &Exit{}}},
		{"agent gone until exit", "exit", []model.State{driveState(claude, model.StateWorking, false, 0), gone}, exit{5, true}, Outcome{Reason: Exited, Exit: &Exit{Code: 5}, Awaited: true}},
		{"closed", "exit", []model.State{gone}, exit{}, Outcome{Reason: Closed}},
		{"unknown exit", "exit", []model.State{unknown}, exit{}, Outcome{Reason: Exited, Exit: &Exit{Unknown: true}}},
		{"unknown exit until done", "done", []model.State{unknown}, exit{}, Outcome{Reason: Exited, Exit: &Exit{Unknown: true}}},
		{"signal", "exit", []model.State{driveState(shell, "", true, -1)}, exit{-1, true}, Outcome{Reason: Exited, Exit: &Exit{Code: -1, Signal: true}, Awaited: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := step(t, tc.until, tc.states, tc.exit.code, tc.exit.ok)
			if !same(got, tc.want) {
				t.Fatalf("%+v %+v, want %+v %+v", got, got.Exit, tc.want, tc.want.Exit)
			}
		})
	}
	// A program's own exit codes 2, 3 and 124 are its exit, never blocked
	// or a timeout.
	for _, code := range []int{2, 3, 124} {
		for _, until := range []string{"done", "exit"} {
			states := []model.State{driveState(shell, model.StateTerminalRunning, false, 0), driveState(shell, "", true, code)}
			want := Outcome{Reason: Exited, Exit: &Exit{Code: code}, Awaited: true}
			if got := step(t, until, states, code, true); !same(got, want) {
				t.Errorf("exit %d until %s: %+v %+v", code, until, got, got.Exit)
			}
		}
	}
}

// step runs a wait over states and returns its last outcome. exit is what
// the exit lookup finds; the lookup must run only for a pane that exited
// or went.
func step(t *testing.T, until string, states []model.State, code int, ok bool) Outcome {
	t.Helper()
	w := NewWait(until)
	var o Outcome
	for i, st := range states {
		looked := false
		o = w.Step(st, "p", func() (int, bool) { looked = true; return code, ok })
		gone := len(st.Panes) == 0 || st.Panes[0].Exited && !st.Panes[0].ExitUnknown
		if looked != gone {
			t.Fatalf("state %d: exit looked up %v, want %v", i, looked, gone)
		}
		if o.Reason != "" && i < len(states)-1 {
			t.Fatalf("state %d: ended early with %+v", i, o)
		}
	}
	return o
}

func same(a, b Outcome) bool {
	if (a.Exit == nil) != (b.Exit == nil) || a.Exit != nil && *a.Exit != *b.Exit {
		return false
	}
	a.Exit, b.Exit = nil, nil
	return a == b
}
