package daemon

import (
	"cmp"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
)

// inboxOf is everything in st that waits on the user, for State.Inbox:
// approvals, questions and plans with the answers their screens offer,
// forms that ask something, then failed CI and agents stopped on an
// error. Answered items leave on the next change, as their activity moves
// on. A window joins in the inboxes of the other machines it shows.
func inboxOf(st *model.State) model.Inbox {
	var in model.Inbox
	title := func(id string) string {
		if w := st.Workspace(id); w != nil {
			if w.NameSet || w.Label == "" {
				return w.Name
			}
			return w.Label
		}
		return ""
	}
	var blocked []model.InboxItem
	for _, a := range st.Activities {
		it := model.InboxItem{WorkspaceID: a.WorkspaceID, PaneID: a.PaneID, At: a.UpdatedAt.UnixNano(), Tab: title(a.WorkspaceID)}
		switch a.State {
		case model.StateWorking, model.StateConnecting:
			in.Moving++
			continue
		case model.StatePendingApproval:
			it.Kind, it.Command = model.InboxApproval, a.Command
			it.Text = cmp.Or(oneLine(a.Detail), "asks permission")
			if a.Command != "" {
				it.Text = "wants to run " + oneLine(a.Command)
			}
		case model.StateAwaitingInput:
			it.Kind, it.Text, it.Answers = model.InboxQuestion, cmp.Or(oneLine(a.Detail), "has a question"), a.Buttons
		case model.StatePlanReady:
			it.Kind, it.Text, it.Answers = model.InboxPlan, "has a plan to approve", a.Buttons
		case model.StateError:
			it.Kind, it.Text = model.InboxBlocked, cmp.Or(oneLine(a.Detail), "stopped on an error")
			blocked = append(blocked, it)
			continue
		default:
			continue
		}
		in.Items = append(in.Items, it)
	}
	for _, f := range st.Forms {
		if f.Waits {
			in.Items = append(in.Items, model.InboxItem{Kind: model.InboxForm, WorkspaceID: f.WorkspaceID, PaneID: f.PaneID,
				Tab: title(f.WorkspaceID), Text: cmp.Or(f.Title, "a form waits"), Form: f.ID})
		}
	}
	var ci []model.InboxItem
	for _, w := range st.Workspaces {
		if pr, ok := st.PRs[w.ID]; ok && pr.State == model.PROpen && pr.CI() == model.CheckFail {
			ci = append(ci, model.InboxItem{Kind: model.InboxCI, WorkspaceID: w.ID, Tab: title(w.ID),
				Text: "CI failed: " + strings.Join(pr.Failed(), ", ")})
		}
	}
	// why: oldest first, so what waited longest is on top; a form has no At and keeps its place.
	slices.SortStableFunc(in.Items, func(a, b model.InboxItem) int { return cmp.Compare(a.At, b.At) })
	in.Items = append(append(in.Items, ci...), blocked...)
	return in
}

// oneLine is s's first line, trimmed.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// phoneInbox is in's items of tab ws, their text redacted for the phone.
func phoneInbox(in model.Inbox, ws string) []model.InboxItem {
	var out []model.InboxItem
	for _, it := range in.Items {
		if it.WorkspaceID == ws {
			it.Text, it.Command = logs.Redact(it.Text), logs.Redact(it.Command)
			it.Answers = slices.Clone(it.Answers)
			for i := range it.Answers {
				it.Answers[i] = logs.Redact(it.Answers[i])
			}
			out = append(out, it)
		}
	}
	return out
}
