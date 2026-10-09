package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestNotifyRule(t *testing.T) {
	all := config.DefaultNotifySettings()
	all.Sound = "bell"
	day := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	night := time.Date(2026, 1, 1, 23, 30, 0, 0, time.Local)
	early := time.Date(2026, 1, 2, 7, 0, 0, 0, time.Local)
	act := func(s model.AgentState, p model.Provider, urgency string) model.Activity {
		return model.Activity{State: s, Provider: p, Urgency: urgency}
	}
	quiet := all
	quiet.QuietFrom, quiet.QuietTo = 22*60, 8*60
	noDone := all
	noDone.Done, noDone.Approval = false, false
	muted := all
	muted.MutedAgents = []string{"codex", "terminal"}
	for i, tc := range []struct {
		r           config.NotifySettings
		a           model.Activity
		at          time.Time
		show, sound bool
	}{
		{all, act(model.StateCompleted, model.ProviderClaude, ""), day, true, true},
		{config.DefaultNotifySettings(), act(model.StateCompleted, model.ProviderClaude, ""), day, true, false}, // no sound set
		{noDone, act(model.StateCompleted, model.ProviderClaude, ""), day, false, false},
		{noDone, act(model.StatePendingApproval, model.ProviderClaude, ""), day, false, false},
		{noDone, act(model.StatePlanReady, model.ProviderClaude, ""), day, false, false},
		{noDone, act(model.StateError, model.ProviderClaude, ""), day, true, true},
		{muted, act(model.StatePendingApproval, model.ProviderCodex, ""), day, false, false},
		{muted, act(model.StateAwaitingInput, model.ProviderTerminal, ""), day, false, false},
		{muted, act(model.StatePendingApproval, model.ProviderClaude, ""), day, true, true},
		// Quiet hours, 22:00-08:00 across midnight: urgent ones only, silent.
		{quiet, act(model.StateCompleted, model.ProviderClaude, ""), night, false, false},
		{quiet, act(model.StateAwaitingInput, model.ProviderClaude, "soon"), early, false, false},
		{quiet, act(model.StatePendingApproval, model.ProviderClaude, ""), night, true, false},
		{quiet, act(model.StateError, model.ProviderClaude, ""), early, true, false},
		{quiet, act(model.StateAwaitingInput, model.ProviderClaude, "now"), night, true, false},
		{quiet, act(model.StateCompleted, model.ProviderClaude, ""), day, true, true},
	} {
		if show, sound := notifyRule(tc.r, tc.a, tc.at); show != tc.show || sound != tc.sound {
			t.Errorf("case %d (%s at %s): show %v sound %v, want %v %v", i, tc.a.State, tc.at.Format("15:04"), show, sound, tc.show, tc.sound)
		}
	}
}

func TestNotifySendArgs(t *testing.T) {
	head := []string{"--app-name=pitwall", "--urgency=critical", "--hint=string:x-canonical-private-synchronous:pitwall-ws"}
	tail := []string{"--", "-t", "Approval: rm"}
	for _, tc := range []struct {
		actions, answer bool
		want            []string
	}{
		{false, false, nil},
		{false, true, nil}, // no actions, no answers
		{true, false, []string{"--wait", "--action=default=Open"}},
		{true, true, []string{"--wait", "--action=default=Open", "--action=allow=Allow", "--action=deny=Deny"}},
	} {
		want := append(append(append([]string{}, head...), tc.want...), tail...)
		if got := notifySendArgs(true, tc.actions, tc.answer, "ws", "-t", "Approval: rm"); !reflect.DeepEqual(got, want) {
			t.Errorf("actions %v answer %v: %q", tc.actions, tc.answer, got)
		}
	}
	if got := notifySendArgs(false, false, false, "ws", "t", "b"); got[1] != "--urgency=normal" {
		t.Errorf("not urgent: %q", got)
	}
}

// A click on a notification shows its pane; Allow and Deny on it send
// the answer for the prompt it was about.
func TestNotificationActions(t *testing.T) {
	b := NewFakeBackend()
	var jumped []model.Activity
	n := &notifier{jump: func(a model.Activity) { jumped = append(jumped, a) }}
	a := model.Activity{PaneID: "p", WorkspaceID: "w", UpdatedAt: time.Unix(5, 6)}
	n.act(b, a, "default")
	if len(jumped) != 1 || jumped[0].PaneID != "p" {
		t.Fatalf("default: jumped %v", jumped)
	}
	n.act(b, a, "allow")
	n.act(b, a, "deny")
	n.act(b, a, "2") // an unknown action does nothing
	var answers []proto.Answer
	for _, m := range b.Sent() {
		if ans, ok := m.(proto.Answer); ok {
			answers = append(answers, ans)
		}
	}
	want := []proto.Answer{{Pane: "p", At: a.UpdatedAt.UnixNano(), Allow: true}, {Pane: "p", At: a.UpdatedAt.UnixNano()}}
	if !reflect.DeepEqual(answers, want) || len(jumped) != 1 {
		t.Fatalf("answers %+v, jumped %d", answers, len(jumped))
	}
}

// allow_prompt and deny_prompt answer the focused pane's prompt, else the
// one the shown tab's row shows, and nothing that is not a known prompt.
func TestAnswerKey(t *testing.T) {
	at := time.Unix(10, 0)
	st := model.State{Activities: []model.Activity{
		{PaneID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StatePendingApproval, UpdatedAt: at},
		{PaneID: "b", WorkspaceID: "w", Provider: model.ProviderCodex, State: model.StatePendingApproval, UpdatedAt: at.Add(time.Second)},
		{PaneID: "c", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: at},
		{PaneID: "d", WorkspaceID: "v", Provider: model.ProviderPi, State: model.StatePendingApproval, UpdatedAt: at},
	}}
	n := &nav{workspace: "w", tab: "t", focus: map[string]string{}}
	focus := func(p string) { n.focus[focusKey("w", "t")] = p }
	for _, tc := range []struct {
		ws, focused string
		allow       bool
		want        any
	}{
		{"w", "a", true, proto.Answer{Pane: "a", At: at.UnixNano(), Allow: true}},
		{"w", "c", false, proto.Answer{Pane: "b", At: at.Add(time.Second).UnixNano()}}, // the row's: the newest approval
		{"v", "d", true, nil}, // pi's prompts are not known
		{"x", "", true, nil},
	} {
		n.workspace = tc.ws
		focus(tc.focused)
		if got := n.answer(&st, tc.allow); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s/%s: %+v, want %+v", tc.ws, tc.focused, got, tc.want)
		}
	}
}
