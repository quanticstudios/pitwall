package board

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

var now = time.Unix(1e9, 0)

// fixture is one tab per case, each with one pane: an agent pane unless
// the provider is "" (a shell).
type fixture struct {
	st model.State
}

func (f *fixture) tab(id string, p model.Provider, w model.Workspace) {
	w.ID, w.SessionID = id, cmpOr(w.SessionID, "s1")
	w.Tabs, w.ActiveTab = []model.Tab{{ID: "t" + id, Layout: &layout.Node{Pane: "p" + id}}}, "t"+id
	f.st.Workspaces = append(f.st.Workspaces, w)
	f.st.Panes = append(f.st.Panes, model.Pane{ID: "p" + id, WorkspaceID: id, Provider: p})
}

func (f *fixture) act(id string, a model.Activity) {
	a.PaneID, a.WorkspaceID = "p"+id, id
	if a.Provider == "" {
		a.Provider = model.ProviderClaude
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now.Add(-time.Minute)
	}
	f.st.Activities = append(f.st.Activities, a)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// where is each column's card ids, comma separated, "-" for an empty one.
func where(b Board) string {
	var out []string
	for c := range b.Cols {
		ids := []string{}
		for _, card := range b.Cols[c] {
			ids = append(ids, card.ID)
		}
		out = append(out, Names[c]+": "+strings.Join(ids, ","))
	}
	return strings.Join(out, "; ")
}

func colOf(t *testing.T, b Board, id string) Column {
	t.Helper()
	for c := range b.Cols {
		if slices.ContainsFunc(b.Cols[c], func(card Card) bool { return card.ID == id }) {
			return Column(c)
		}
	}
	return -1
}

// TestColumns puts every state in its column.
func TestColumns(t *testing.T) {
	claude := model.ProviderClaude
	for _, c := range []struct {
		name string
		set  func(f *fixture)
		want Column
		kind Kind
	}{
		{"approval", func(f *fixture) { f.act("x", model.Activity{State: model.StatePendingApproval}) }, NeedsYou, KindApproval},
		{"question", func(f *fixture) { f.act("x", model.Activity{State: model.StateAwaitingInput}) }, NeedsYou, KindInput},
		{"plan", func(f *fixture) { f.act("x", model.Activity{State: model.StatePlanReady}) }, NeedsYou, KindPlan},
		{"error", func(f *fixture) { f.act("x", model.Activity{State: model.StateError, Detail: "API error"}) }, Failed, KindError},
		{"usage limit in the detail", func(f *fixture) {
			f.act("x", model.Activity{State: model.StateError, Detail: "You've hit your session limit · resets 3pm"})
		}, Failed, KindLimit},
		{"usage limit with a resume", func(f *fixture) {
			f.act("x", model.Activity{State: model.StateCompleted, Resumes: now.Add(time.Hour)})
		}, Failed, KindLimit},
		{"working", func(f *fixture) { f.act("x", model.Activity{State: model.StateWorking}) }, Working, KindWorking},
		{"connecting", func(f *fixture) { f.act("x", model.Activity{State: model.StateConnecting}) }, Working, KindWorking},
		{"pr open", func(f *fixture) { f.act("x", model.Activity{State: model.StatePROpen}) }, PROpen, KindPR},
		{"done", func(f *fixture) { f.act("x", model.Activity{State: model.StateCompleted}) }, Done, KindDone},
		{"idle agent", func(f *fixture) {}, Done, KindIdle},
		{"approval beats work in one tab", func(f *fixture) {
			f.st.Panes = append(f.st.Panes, model.Pane{ID: "q", WorkspaceID: "x", Provider: claude})
			f.act("x", model.Activity{State: model.StateWorking})
			f.st.Activities = append(f.st.Activities, model.Activity{PaneID: "q", WorkspaceID: "x", Provider: claude, State: model.StatePendingApproval})
		}, NeedsYou, KindApproval},
	} {
		t.Run(c.name, func(t *testing.T) {
			var f fixture
			f.tab("x", claude, model.Workspace{})
			c.set(&f)
			b := Derive(&f.st, Options{Now: now, Session: "s1"})
			if got := colOf(t, b, "x"); got != c.want || b.All["x"].Kind != c.kind {
				t.Errorf("column %v kind %v, want %v %v; %s", got, b.All["x"].Kind, c.want, c.kind, where(b))
			}
		})
	}
}

// TestShells: a plain shell, even one running a command, hides unless
// Shells is on; one whose command reports its status (OSC 7501) shows as
// running; a worker tab whose agent exited is Stopped in Done.
func TestShells(t *testing.T) {
	var f fixture
	f.tab("sh", "", model.Workspace{})
	f.tab("dev", "", model.Workspace{})
	f.st.Activities = append(f.st.Activities, model.Activity{PaneID: "pdev", WorkspaceID: "dev", Provider: model.ProviderTerminal, State: model.StateTerminalRunning, Detail: "npm"})
	f.tab("make", "", model.Workspace{})
	f.st.Panes[len(f.st.Panes)-1].Program = &model.Program{State: "working", Progress: 60, Message: "make: 3 of 5"}
	f.tab("o", model.ProviderClaude, model.Workspace{})
	f.act("o", model.Activity{State: model.StateWorking})
	f.tab("gone", "", model.Workspace{Parent: "o"})

	b := Derive(&f.st, Options{Now: now, Session: "s1"})
	if got := where(b); got != "Needs you: ; Failed: ; Working: o,make; PR open: ; Done: ; Queued: ; Shells: " {
		t.Errorf("shells hidden: %s", got)
	}
	if b.HiddenShells != 2 {
		t.Errorf("hidden shells %d, want 2", b.HiddenShells)
	}
	if k := b.All["gone"].Kind; k != KindStopped || b.All["o"].Fleet[Done] != 1 {
		t.Errorf("stopped worker: kind %v, fleet %v", k, b.All["o"].Fleet)
	}
	b = Derive(&f.st, Options{Now: now, Session: "s1", Shells: true})
	if got := strings.Join([]string{b.Cols[Shells][0].ID, b.Cols[Shells][1].ID}, ","); len(b.Cols[Shells]) != 2 || got != "sh,dev" && got != "dev,sh" {
		t.Errorf("shells shown: %s", where(b))
	}
}

// TestFolding: a worker folds into its orchestrator while it works, has
// a PR open or is done, and shows on its own, naming its orchestrator,
// while it needs the user or failed. Every worker counts in the fleet.
func TestFolding(t *testing.T) {
	var f fixture
	f.tab("o", model.ProviderClaude, model.Workspace{})
	f.act("o", model.Activity{State: model.StateCompleted})
	for id, s := range map[string]model.AgentState{"w1": model.StateWorking, "w2": model.StatePendingApproval, "w3": model.StateError, "w4": model.StatePROpen, "w5": model.StateCompleted} {
		f.tab(id, model.ProviderCodex, model.Workspace{Parent: "o"})
		f.act(id, model.Activity{State: s})
	}
	b := Derive(&f.st, Options{Now: now, Session: "s1"})
	if got := where(b); got != "Needs you: w2; Failed: w3; Working: o; PR open: ; Done: ; Queued: ; Shells: " {
		t.Errorf("got %s", got)
	}
	o := b.All["o"]
	if o.Kind != KindWatching {
		t.Errorf("orchestrator with a working worker is %v, want watching", o.Kind)
	}
	if want := [Columns]int{NeedsYou: 1, Failed: 1, Working: 1, PROpen: 1, Done: 1}; o.Fleet != want || len(o.Workers) != 5 {
		t.Errorf("fleet %v workers %v", o.Fleet, o.Workers)
	}
	if b.All["w2"].Parent != "o" {
		t.Errorf("worker on its own names no parent")
	}
}

// TestScope narrows the board to the window's session by default, or to
// every session, a group, the ungrouped tabs or a host,
// and leaves out snoozed and settled tabs, counting them.
func TestScope(t *testing.T) {
	var f fixture
	f.tab("a", model.ProviderClaude, model.Workspace{ProjectID: "g1"})
	f.tab("b", model.ProviderClaude, model.Workspace{ProjectID: "g2"})
	f.tab("c", model.ProviderClaude, model.Workspace{})
	f.tab("r", model.ProviderClaude, model.Workspace{SessionID: "remote"})
	f.tab("z", model.ProviderClaude, model.Workspace{SnoozedUntil: now.Add(time.Hour)})
	f.tab("y", model.ProviderClaude, model.Workspace{Settled: now.Add(-time.Hour)})
	f.tab("d", model.ProviderClaude, model.Workspace{Detached: true})
	for _, id := range []string{"a", "b", "c", "r", "z", "y", "d"} {
		f.act(id, model.Activity{State: model.StateWorking})
	}
	f.st.Tasks = []model.Task{{ID: "t1", SessionID: "s1", GroupID: "g1"}, {ID: "t2", SessionID: "s1"}}
	hosts := map[string]string{"remote": "box"}
	for _, c := range []struct {
		scope Scope
		want  string
	}{
		{Scope{}, "a,b,c|task:t1,task:t2"},
		{Scope{ScopeAll, ""}, "a,b,c,r|task:t1,task:t2"},
		{Scope{ScopeProject, "g1"}, "a|task:t1"},
		{Scope{ScopeUngrouped, ""}, "c|task:t2"},
		{Scope{ScopeHost, "box"}, "r|"},
		{Scope{ScopeHost, ""}, "a,b,c|task:t1,task:t2"},
	} {
		b := Derive(&f.st, Options{Now: now, Session: "s1", Scope: c.scope, Hosts: hosts})
		var ids, tasks []string
		for _, card := range b.Cols[Working] {
			ids = append(ids, card.ID)
		}
		for _, card := range b.Cols[Queued] {
			tasks = append(tasks, card.ID)
		}
		slices.Sort(ids)
		if got := strings.Join(ids, ",") + "|" + strings.Join(tasks, ","); got != c.want {
			t.Errorf("scope %v: %s, want %s", c.scope, got, c.want)
		}
		if c.scope.Kind == ScopeAll && (b.Snoozed != 1 || b.Settled != 1) {
			t.Errorf("snoozed %d settled %d, want 1 1", b.Snoozed, b.Settled)
		}
	}
	if b := Derive(&f.st, Options{Now: now, Scope: Scope{Kind: ScopeAll}, Hosts: hosts}); b.All["r"].Host != "box" {
		t.Errorf("remote card's host %q", b.All["r"].Host)
	}
}

// TestSharedScope: a shared session's tab on another host is that host's
// by its tab id, though its session is this machine's.
func TestSharedScope(t *testing.T) {
	var f fixture
	f.tab("l", model.ProviderClaude, model.Workspace{SessionID: "s1"})
	f.tab("b", model.ProviderClaude, model.Workspace{SessionID: "s1"})
	f.act("l", model.Activity{State: model.StateWorking})
	f.act("b", model.Activity{State: model.StateWorking})
	hosts := map[string]string{"b": "box"}
	for scope, want := range map[Scope]string{{ScopeHost, "box"}: "b", {ScopeHost, ""}: "l", {ScopeUngrouped, ""}: "l", {}: "b,l"} {
		var ids []string
		for _, c := range Derive(&f.st, Options{Now: now, Session: "s1", Scope: scope, Hosts: hosts}).Cols[Working] {
			ids = append(ids, c.ID)
		}
		slices.Sort(ids)
		if got := strings.Join(ids, ","); got != want {
			t.Errorf("scope %v: %s, want %s", scope, got, want)
		}
	}
	if b := Derive(&f.st, Options{Now: now, Session: "s1", Hosts: hosts}); b.All["b"].Host != "box" || b.All["l"].Host != "" {
		t.Errorf("hosts %q %q", b.All["b"].Host, b.All["l"].Host)
	}
}

// TestOrder: Needs you is approvals, questions, then plans, oldest first;
// Working has quiet agents on top; Done has unseen turns on top.
func TestOrder(t *testing.T) {
	var f fixture
	add := func(id string, a model.Activity, ago time.Duration) {
		f.tab(id, model.ProviderClaude, model.Workspace{})
		a.UpdatedAt = now.Add(-ago)
		f.act(id, a)
	}
	add("plan", model.Activity{State: model.StatePlanReady}, 9*time.Minute)
	add("q", model.Activity{State: model.StateAwaitingInput}, time.Minute)
	add("new", model.Activity{State: model.StatePendingApproval}, time.Minute)
	add("old", model.Activity{State: model.StatePendingApproval}, 5*time.Minute)
	add("busy", model.Activity{State: model.StateWorking}, time.Minute)
	add("still", model.Activity{State: model.StateWorking, QuietSince: now.Add(-20 * time.Minute)}, 30*time.Minute)
	add("seen", model.Activity{State: model.StateCompleted}, time.Minute)
	add("unseen", model.Activity{State: model.StateCompleted, Unseen: true}, 40*time.Minute)
	b := Derive(&f.st, Options{Now: now, Session: "s1"})
	if got := where(b); got != "Needs you: old,new,q,plan; Failed: ; Working: still,busy; PR open: ; Done: unseen,seen; Queued: ; Shells: " {
		t.Errorf("got %s", got)
	}
}

// TestAnswers: a short one-line command gets Allow and Deny; a long or
// multi-line one, one on the guard's deny list or [agents] guard_deny, or
// none known, sends the user to the tab.
func TestAnswers(t *testing.T) {
	approval := func(cmd string) Card {
		return Card{Kind: KindApproval, Act: model.Activity{State: model.StatePendingApproval, Provider: model.ProviderClaude, Command: cmd}}
	}
	for _, c := range []struct {
		cmd  string
		want Answer
	}{
		{"npm run migrate -- --env staging", AnswerAllow},
		{"", AnswerUnknown}, // another tool, or an older daemon: nothing to check
		{strings.Repeat("x", MaxCommand), AnswerAllow},
		{strings.Repeat("x", MaxCommand+1), AnswerLong},
		{"echo a\necho b", AnswerLong},
		{"git push origin main", AnswerGuard},
		{"go test ./... && gh pr merge 3", AnswerGuard},
		// rm is on no built-in list; guard_deny can add it.
		{"rm -rf ./checkpoints/old && python train.py --resume latest --epochs 3", AnswerAllow},
	} {
		if got := approval(c.cmd).Answers(); got != c.want {
			t.Errorf("%q: %v, want %v", c.cmd, got, c.want)
		}
	}
	var f fixture
	f.tab("x", model.ProviderClaude, model.Workspace{})
	f.act("x", model.Activity{State: model.StatePendingApproval, Command: "rm -rf ./checkpoints/old && python train.py"})
	for _, c := range []struct {
		deny []string
		want Answer
	}{{nil, AnswerAllow}, {[]string{"rm -rf *"}, AnswerGuard}, {[]string{"npm publish *"}, AnswerAllow}} {
		b := Derive(&f.st, Options{Now: now, Session: "s1", GuardDeny: c.deny})
		if got := b.All["x"].Answers(); got != c.want {
			t.Errorf("guard_deny %q: %v, want %v", c.deny, got, c.want)
		}
	}
	chat := approval("ls")
	chat.Tab.Chat = "c1"
	if got := chat.Answers(); got != AnswerChat {
		t.Errorf("a managed chat's approval offers %v, want the tab", got)
	}
	if got := (Card{Kind: KindInput}).Answers(); got != AnswerNone {
		t.Errorf("a question offers %v", got)
	}
}

// TestHold: a card under the pointer or focus stays where it was, as it
// was, with its new kind noted, until it is let go; a held card whose tab
// closed goes at once.
func TestHold(t *testing.T) {
	var f fixture
	f.tab("a", model.ProviderClaude, model.Workspace{})
	f.act("a", model.Activity{State: model.StatePendingApproval, Command: "ls"})
	f.tab("b", model.ProviderClaude, model.Workspace{})
	f.act("b", model.Activity{State: model.StatePendingApproval})
	var tr Tracker
	frame := func(held ...string) Board {
		b := Derive(&f.st, Options{Now: now, Session: "s1"})
		h := map[string]bool{}
		for _, id := range held {
			h[id] = true
		}
		tr.Hold(&b, h, now)
		return b
	}
	frame("a")
	f.st.Activities[0].State = model.StateWorking // answered: the agent goes on
	b := frame("a")
	if got := where(b); got != "Needs you: a,b; Failed: ; Working: ; PR open: ; Done: ; Queued: ; Shells: " {
		t.Fatalf("held: %s", got)
	}
	if a := b.Cols[NeedsYou][0]; !a.Held || a.Now != KindWorking || a.Kind != KindApproval || a.Act.Command != "ls" {
		t.Errorf("held card %+v", a)
	}
	b = frame("a")
	if !b.Cols[NeedsYou][0].Held {
		t.Errorf("let go while still held: %s", where(b))
	}
	b = frame()
	if got := where(b); got != "Needs you: b; Failed: ; Working: a; PR open: ; Done: ; Queued: ; Shells: " {
		t.Errorf("let go: %s", got)
	}
	// The card under the pointer moved on and its tab closed.
	frame("b")
	f.st.Workspaces, f.st.Activities = f.st.Workspaces[:1], f.st.Activities[:1]
	if b = frame("b"); len(b.Cols[NeedsYou]) != 0 {
		t.Errorf("a closed tab's card held: %s", where(b))
	}
}

// TestArming: a card that just arrived in Needs you, or whose ask changed,
// takes no clicks for Arming.
func TestArming(t *testing.T) {
	var f fixture
	f.tab("a", model.ProviderClaude, model.Workspace{})
	f.act("a", model.Activity{State: model.StatePendingApproval})
	var tr Tracker
	at := func(d time.Duration) {
		b := Derive(&f.st, Options{Now: now, Session: "s1"})
		tr.Hold(&b, nil, now.Add(d))
	}
	at(0)
	if tr.Armed("a", now.Add(Arming-time.Millisecond)) {
		t.Errorf("armed before %v", Arming)
	}
	if !tr.Armed("a", now.Add(Arming)) {
		t.Errorf("not armed after %v", Arming)
	}
	at(time.Second)
	if !tr.Armed("a", now.Add(time.Second)) {
		t.Errorf("same ask lost its arming")
	}
	f.st.Activities[0].UpdatedAt = now // the next prompt, in the same card
	at(2 * time.Second)
	if tr.Armed("a", now.Add(2*time.Second+Arming/2)) {
		t.Errorf("a new ask is armed at once")
	}
	if tr.Armed("nope", now.Add(time.Hour)) {
		t.Errorf("an unknown card is armed")
	}
}

// TestHoldPlace: when an approval above leaves, the card under the pointer
// keeps its place, and the card that slides into another's place takes no
// clicks for Arming, so a click meant for one never answers another.
func TestHoldPlace(t *testing.T) {
	var f fixture
	for i, id := range []string{"a", "b", "c"} {
		f.tab(id, model.ProviderClaude, model.Workspace{})
		f.act(id, model.Activity{State: model.StatePendingApproval, UpdatedAt: now.Add(time.Duration(i-10) * time.Minute)})
	}
	var tr Tracker
	frame := func(at time.Duration, held ...string) Board {
		b := Derive(&f.st, Options{Now: now, Session: "s1"})
		h := map[string]bool{}
		for _, id := range held {
			h[id] = true
		}
		tr.Hold(&b, h, now.Add(at))
		return b
	}
	frame(0, "b")
	frame(time.Second, "b")
	f.st.Activities[0].State = model.StateWorking // a is answered elsewhere
	b := frame(2*time.Second, "b")
	if got := where(b); got != "Needs you: c,b; Failed: ; Working: a; PR open: ; Done: ; Queued: ; Shells: " {
		t.Fatalf("b under the pointer: %s", got)
	}
	if b.Cols[NeedsYou][1].Held {
		t.Errorf("b, which nothing answered, is held as answered")
	}
	at := now.Add(2*time.Second + Arming/2)
	if tr.Armed("c", at) {
		t.Errorf("c took b's old place and is armed at once")
	}
	if !tr.Armed("b", at) {
		t.Errorf("b kept its place and lost its arming")
	}
	b = frame(3 * time.Second) // the pointer leaves
	if got := where(b); got != "Needs you: b,c; Failed: ; Working: a; PR open: ; Done: ; Queued: ; Shells: " || tr.Armed("b", now.Add(3*time.Second+Arming/2)) {
		t.Errorf("let go: %s, b armed %v", got, tr.Armed("b", now.Add(3*time.Second+Arming/2)))
	}
}
