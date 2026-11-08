package app

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// OpenBoard opens the board on the window's first frame; the demo sets it.
var OpenBoard bool

// FakeBoard is a fake backend whose state fills the board: an
// orchestrator with seven workers, agents in every state across three
// groups, a remote host, queued tasks and two shells. busy adds a second
// fleet and more asks, about 31 agents in all.
type FakeBoard struct{ *FakeBackend }

// NewFakeBoard returns the board's demo state, which does not tick.
func NewFakeBoard(busy bool) *FakeBoard {
	f := &FakeBoard{NewFakeBackend()}
	now := time.Now()
	st := model.State{
		Sessions: []model.Session{{ID: "s1", Name: "daily-work", UsedAt: now}, {ID: "s9", Name: "evals", UsedAt: now}},
		Projects: []model.Project{
			{ID: "acme", SessionID: "s1", Name: "acme-api", Root: fakeHome + "/src/acme-api", Kind: model.ProjectGit, Color: "blue"},
			{ID: "bill", SessionID: "s1", Name: "billing", Root: fakeHome + "/src/billing", Kind: model.ProjectGit, Color: "green"},
			{ID: "web", SessionID: "s1", Name: "web-app", Root: fakeHome + "/src/web-app", Kind: model.ProjectGit, Color: "violet"},
		},
		Stats: map[string]model.BranchStats{},
		PRs:   map[string]model.PR{},
	}
	n := 0
	type tab struct {
		id, title, project, branch, dir, parent string
		p                                       model.Provider
		state                                   model.AgentState
		ago                                     time.Duration
		add, del                                int
		a                                       model.Activity
	}
	add := func(t tab) {
		n++
		pane := "p" + t.id
		w := model.Workspace{ID: t.id, SessionID: "s1", ProjectID: t.project, Label: t.title, Branch: t.branch, Path: t.dir, Parent: t.parent,
			Tabs: []model.Tab{{ID: "t" + t.id, Title: t.title, Layout: &layout.Node{Pane: pane}}}, ActiveTab: "t" + t.id, UpdatedAt: now.Add(-t.ago)}
		if strings.HasPrefix(t.id, "h") || strings.HasPrefix(t.id, "x") {
			w.SessionID = "s9"
		}
		if w.Path == "" {
			w.Path = fakeHome + "/src/" + cmp.Or(projectOf(st, t.project), "x") + "/.worktrees/" + t.branch
		}
		st.Workspaces = append(st.Workspaces, w)
		st.Panes = append(st.Panes, model.Pane{ID: pane, WorkspaceID: t.id, Cwd: w.Path, Provider: t.p})
		if t.add > 0 || t.del > 0 {
			st.Stats[t.id] = model.BranchStats{Additions: t.add, Deletions: t.del}
		}
		if t.state == "" {
			return
		}
		a := t.a
		a.PaneID, a.WorkspaceID, a.Provider, a.State, a.UpdatedAt = pane, t.id, t.p, t.state, now.Add(-t.ago)
		if t.p == "" {
			a.Provider = model.ProviderTerminal
		}
		st.Activities = append(st.Activities, a)
	}
	claude, codex, pi := model.ProviderClaude, model.ProviderCodex, model.ProviderPi
	m := time.Minute
	add(tab{id: "t1", title: "Write the onboarding doc", p: claude, state: model.StateCompleted, dir: fakeHome + "/notes", ago: 8 * m,
		a: model.Activity{Detail: "Wrote docs/onboarding.md with four sections and a checklist.", Unseen: true}})
	add(tab{id: "t2", title: "make release", dir: fakeHome + "/src/tools", ago: 3 * m})
	st.Panes[len(st.Panes)-1].Program = &model.Program{App: "make", State: "working", Progress: 60, Message: "building 3 of 5 targets", UpdatedAt: now.Add(-3 * m)}
	add(tab{id: "t3", title: "Explain the cache layer", p: codex, state: model.StateCompleted, dir: fakeHome + "/scratch/cache-notes", ago: 62 * m,
		a: model.Activity{Detail: "Summarised the two cache tiers and when each one is skipped."}})
	add(tab{id: "t4", title: "~", dir: fakeHome, ago: 2 * time.Hour})
	add(tab{id: "o1", title: "Rate limits v2", p: claude, state: model.StateCompleted, project: "acme", branch: "main", dir: fakeHome + "/src/acme-api", ago: 24 * m,
		a: model.Activity{Detail: "Waiting on 5 workers. Merging the middleware first, then the store adapter on top."}})
	add(tab{id: "w1", title: "Migrate limits table", p: codex, state: model.StatePendingApproval, project: "acme", branch: "limits-table", parent: "o1", ago: 40 * time.Second, add: 188, del: 42,
		a: model.Activity{Detail: "Run the migration", Command: "npm run migrate -- --env staging", Buttons: []string{"Yes, and don't ask again"}}})
	add(tab{id: "w2", title: "Limiter tests", p: codex, state: model.StateAwaitingInput, project: "acme", branch: "limiter-tests", parent: "o1", ago: 130 * time.Second, add: 96,
		a: model.Activity{Detail: "Should a 429 response carry Retry-After, and in which unit?", Buttons: []string{"Seconds", "HTTP date", "Leave it out"}}})
	add(tab{id: "w3", title: "Token bucket middleware", p: codex, state: model.StateWorking, project: "acme", branch: "token-bucket", parent: "o1", ago: 9 * m, add: 214, del: 37})
	add(tab{id: "w4", title: "Redis store adapter", p: codex, state: model.StateWorking, project: "acme", branch: "redis-store", parent: "o1", ago: 7 * m, add: 120})
	add(tab{id: "w5", title: "Docs for limits", p: claude, state: model.StateWorking, project: "acme", branch: "limits-docs", parent: "o1", ago: 3 * m, add: 40})
	add(tab{id: "w6", title: "Fix flaky auth test", p: codex, state: model.StatePROpen, project: "acme", branch: "flaky-auth", parent: "o1", ago: 12 * m, add: 18, del: 9})
	st.PRs["w6"] = model.PR{Number: 142, State: model.PROpen, Review: model.ReviewApproved, Checks: checks(9, 9, 0)}
	add(tab{id: "w7", title: "Read router and middleware", p: codex, state: model.StateCompleted, project: "acme", branch: "read-only", parent: "o1", ago: 18 * m,
		a: model.Activity{Detail: "Mapped every route that needs a limit; 14 public, 3 internal."}})
	add(tab{id: "b1", title: "Stripe v3 webhooks", p: claude, state: model.StatePlanReady, project: "bill", branch: "feat/stripe-v3", ago: 4 * m,
		a: model.Activity{Detail: "Move webhooks to the Stripe v3 events", Buttons: []string{"Yes, and auto-accept edits", "Yes, and approve edits", "No, keep planning"}}})
	st.Workspaces[len(st.Workspaces)-1].Pinned = true
	add(tab{id: "b2", title: "Invoice PDF fonts", p: codex, state: model.StateAwaitingInput, project: "bill", branch: "fix/invoice-fonts", ago: 70 * time.Second, add: 6, del: 2,
		a: model.Activity{Detail: "Which font should the invoice PDF embed?", Buttons: []string{"Inter", "Noto Sans", "Keep the fallback"}}})
	add(tab{id: "b3", title: "Invoice totals rounding", p: claude, state: model.StateCompleted, project: "bill", branch: "fix/rounding", ago: 52 * m, add: 31, del: 12,
		a: model.Activity{Detail: "Totals now round half to even; 6 tests added."}})
	add(tab{id: "c1", title: "Settings page redesign", p: claude, state: model.StateWorking, project: "web", branch: "feat/settings", ago: 6 * m, add: 402, del: 188,
		a: model.Activity{Detail: "Ran npm test, 41 passed, 0 failed"}})
	add(tab{id: "c2", title: "Lint fix pass", p: codex, state: model.StateError, project: "web", branch: "fix/lint", ago: 9 * m, add: 12, del: 30,
		a: model.Activity{Detail: "codex exited with code 1."}})
	add(tab{id: "c3", title: "Bump web-app dependencies", p: claude, state: model.StatePROpen, project: "web", branch: "deps/nov", ago: 35 * m, add: 88, del: 91})
	st.PRs["c3"] = model.PR{Number: 138, State: model.PROpen, Checks: checks(7, 5, 2)}
	add(tab{id: "c4", title: "npm run dev", project: "web", branch: "main", dir: fakeHome + "/src/web-app", ago: 40 * m, state: model.StateTerminalRunning, a: model.Activity{Detail: "npm"}})
	resets := now.Add(40 * m).Truncate(m)
	add(tab{id: "h1", title: "Nightly eval sweep", p: codex, state: model.StateError, dir: fakeHome + "/evals", ago: 20 * m,
		a: model.Activity{Detail: "You've hit your usage limit. Try again at " + resets.Format("3:04 PM") + ".", Resumes: resets.Add(m)}})
	st.Tasks = []model.Task{
		{ID: "q1", SessionID: "s1", GroupID: "bill", Worktree: "retry-budget", Cmd: []string{"claude", "Retry budget for webhooks"}},
		{ID: "q2", SessionID: "s1", GroupID: "acme", Dir: fakeHome + "/src/acme-api", Cmd: []string{"claude", "Nightly dependency audit"}, NextStart: now.Truncate(24 * time.Hour).Add(26 * time.Hour), Repeat: 24 * time.Hour},
	}
	if busy {
		add(tab{id: "o2", title: "Offline sync", p: claude, state: model.StateWorking, branch: "feat/offline", dir: fakeHome + "/src/mobile", ago: 48 * m,
			a: model.Activity{Detail: "Spawned worker 6 of 6, reviewing the conflict resolver next"}})
		for i, w := range []struct {
			title string
			p     model.Provider
			s     model.AgentState
			d     string
		}{
			{"Conflict resolver", codex, model.StateWorking, ""},
			{"SQLite schema v4", codex, model.StateError, "migration 0042 failed: duplicate column sync_at"},
			{"Background fetch on iOS", claude, model.StateWorking, ""},
			{"Retry queue", pi, model.StatePROpen, ""},
			{"Sync status banner", claude, model.StateCompleted, "Banner shows last sync time and a retry link."},
			{"Android work manager", codex, model.StateWorking, ""},
		} {
			id := fmt.Sprintf("m%d", i)
			add(tab{id: id, title: w.title, p: w.p, state: w.s, parent: "o2", branch: strings.ToLower(strings.ReplaceAll(w.title, " ", "-")), dir: fakeHome + "/src/mobile", ago: time.Duration(10+i*5) * m, add: 40 + i*23, del: i * 7, a: model.Activity{Detail: w.d}})
			if i == 5 {
				st.Activities[len(st.Activities)-1].QuietSince = now.Add(-24 * m)
			}
		}
		add(tab{id: "m6", title: "Push token refresh", parent: "o2", branch: "push-tokens", dir: fakeHome + "/src/mobile", ago: 41 * m})
		add(tab{id: "n2", title: "Backfill invoice ids", p: claude, state: model.StatePendingApproval, project: "bill", branch: "fix/backfill", ago: 210 * time.Second, add: 14,
			a: model.Activity{Detail: "Backfill external ids", Command: `psql "$DATABASE_URL" -c "UPDATE invoices SET external_id = concat('inv_', lpad(id::text, 8, '0')) WHERE external_id IS NULL AND created_at < '2026-10-01'" && psql "$DATABASE_URL" -c 'SELECT count(*) FROM invoices WHERE external_id IS NULL'`}})
		add(tab{id: "m9", title: "App icon variants", p: claude, state: model.StatePendingApproval, branch: "design/icons", dir: fakeHome + "/src/mobile", ago: 15 * time.Second, add: 4,
			a: model.Activity{Detail: "Write assets/icon/AppIcon-dark.png"}})
		add(tab{id: "e1", title: "Billing CSV export", p: pi, state: model.StateWorking, project: "bill", branch: "feat/csv", ago: 260 * time.Second, add: 77, del: 3})
		add(tab{id: "e2", title: "Dunning emails copy", p: claude, state: model.StateCompleted, project: "bill", branch: "copy/dunning", ago: 15 * m, add: 22, del: 18,
			a: model.Activity{Detail: "Rewrote the three reminder emails; tone is calmer, links unchanged.", Unseen: true}})
		add(tab{id: "e3", title: "Dark mode tokens", p: claude, state: model.StateWorking, project: "web", branch: "feat/dark-tokens", ago: 7 * m, add: 140, del: 66})
		add(tab{id: "e4", title: "Search page tests", p: codex, state: model.StateCompleted, project: "web", branch: "test/search", ago: 22 * m, add: 64,
			a: model.Activity{Detail: "Added 9 tests; one is skipped until the API returns facets."}})
		add(tab{id: "x5", title: "GPU smoke test", p: codex, state: model.StateWorking, dir: fakeHome + "/evals", ago: 5 * m})
		add(tab{id: "x6", title: "Tokenizer retrain", p: claude, state: model.StatePendingApproval, dir: fakeHome + "/models/tok", ago: 95 * time.Second,
			a: model.Activity{Detail: "Retrain", Command: "rm -rf ./checkpoints/old && python train.py --resume latest --epochs 3"}})
		add(tab{id: "e7", title: "Read the RFC draft", p: claude, state: model.StateCompleted, dir: fakeHome + "/notes/rfc", ago: 90 * m,
			a: model.Activity{Detail: "Listed four open questions in rfc-notes.md."}})
	}
	for i := range st.Activities {
		if a := &st.Activities[i]; model.NeedsYou(a.State) && a.State != model.StateCompleted {
			a.Unseen = true
		}
	}
	st.Version = 1
	f.mu.Lock()
	f.st = st
	f.mu.Unlock()
	return f
}

func projectOf(st model.State, id string) string {
	for _, p := range st.Projects {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// checks is n checks: pass passed, run running, the rest failed.
func checks(n, pass, run int) []model.Check {
	var out []model.Check
	for i := range n {
		s := model.CheckFail
		switch {
		case i < pass:
			s = model.CheckPass
		case i < pass+run:
			s = model.CheckPending
		}
		out = append(out, model.Check{Name: fmt.Sprint("check ", i), State: s})
	}
	return out
}

// SessionHost puts the evals session on a host named box.
func (f *FakeBoard) SessionHost(session string) string {
	if session == "s9" {
		return "box"
	}
	return ""
}

// Hosts is this machine and box.
func (f *FakeBoard) Hosts() []sidebar.HostInfo {
	return []sidebar.HostInfo{{Up: true, Sessions: []string{"s1"}}, {Name: "box", Up: true, Sessions: []string{"s9"}}}
}
