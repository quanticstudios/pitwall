package sidebar

import (
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// reviewerNames are the agents that give a second opinion, by name.
var reviewerNames = map[model.Provider]string{model.ProviderClaude: "Claude Code", model.ProviderCodex: "Codex"}

// ReviewerName is reviewer's name, "Claude Code" or "Codex".
func ReviewerName(reviewer model.Provider) string { return reviewerNames[reviewer] }

// Reviewed is the pane whose changes a second opinion on tab ws reviews:
// the first of its live agent panes to open, which a reviewer never is.
// It is nil when the tab runs no agent.
func Reviewed(st *model.State, ws model.Workspace) *model.Pane {
	for i, p := range st.Panes {
		if p.WorkspaceID == ws.ID && !p.Exited && isAgent(p.Provider) {
			return &st.Panes[i]
		}
	}
	return nil
}

// Reviewers are the agents that can give tab ws's agent a second opinion,
// the first the default: the other of Claude Code and Codex, or both for
// any other agent.
func Reviewers(st *model.State, ws model.Workspace) []model.Provider {
	if p := Reviewed(st, ws); p != nil {
		if r := agent.Reviewer(p.Provider); r != "" {
			return []model.Provider{r}
		}
	}
	return []model.Provider{model.ProviderClaude, model.ProviderCodex}
}

// OpinionBlocked says why a reviewer cannot give tab ws's agent a second
// opinion, "" when it can, in a few words for the menu's right edge.
// installed is whether the reviewer's CLI is on PATH.
func OpinionBlocked(st *model.State, ws model.Workspace, installed bool) string {
	p := Reviewed(st, ws)
	switch {
	case p == nil:
		return "no agent"
	case !agent.HooksFor(p.Provider) || p.HooksMissing:
		return "no hooks" // pitwall cannot tell when its turn ends
	case !installed:
		return "not installed"
	}
	return ""
}

// opinionAct is each reviewer's menu action.
var opinionAct = map[model.Provider]int{model.ProviderClaude: actOpinionClaude, model.ProviderCodex: actOpinionCodex}

// opinionEntries are tab ws's Second opinion menu entries, one per reviewer.
func (s *Sidebar) opinionEntries(v *view, ws model.Workspace) []menuEntry {
	var out []menuEntry
	for _, r := range Reviewers(v.st, ws) {
		why := OpinionBlocked(v.st, ws, s.Installed[r])
		out = append(out, menuEntry{c: &s.menuItem[opinionAct[r]], icon: icEye, text: "Second opinion from " + ReviewerName(r), off: why != "", hint: why})
	}
	return out
}

// opinionClick is the SecondOpinion event of a click on one of tab ws's
// Second opinion entries this frame, nil for none or an off entry.
func (s *Sidebar) opinionClick(gtx layout.Context, v *view, ws model.Workspace) Event {
	for r, act := range opinionAct {
		if s.menuItem[act].Clicked(gtx) && OpinionBlocked(v.st, ws, s.Installed[r]) == "" {
			return SecondOpinion{WorkspaceID: ws.ID, Agent: r}
		}
	}
	return nil
}
