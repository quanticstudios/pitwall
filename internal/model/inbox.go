package model

import (
	"strconv"
	"strings"
)

// InboxKind is what an inbox item waits for.
type InboxKind string

const (
	InboxApproval InboxKind = "approval" // a permission prompt: Deny or Allow
	InboxQuestion InboxKind = "question" // a question, its answers in Answers
	InboxPlan     InboxKind = "plan"     // a plan to approve, its answers in Answers
	InboxForm     InboxKind = "form"     // a pitwall form that asks something
	InboxCI       InboxKind = "ci"       // the tab's pull request failed CI
	InboxBlocked  InboxKind = "blocked"  // the agent stopped on an error or a usage limit
)

// ForYou reports whether k waits on an answer from the user, rather than
// on the user looking at a failure.
func (k InboxKind) ForYou() bool {
	return k == InboxApproval || k == InboxQuestion || k == InboxPlan || k == InboxForm
}

// InboxItem is one thing that waits on the user, in one tab.
type InboxItem struct {
	Kind        InboxKind `json:"kind"`
	WorkspaceID string    `json:"tab"`
	PaneID      string    `json:"pane,omitempty"`
	// At is the activity's UpdatedAt in Unix nanoseconds, which an answer
	// names, as proto.Answer and proto.Press do; 0 for CI and forms.
	At   int64  `json:"at,string,omitempty"`
	Tab  string `json:"name"` // the tab's title
	Text string `json:"text"` // what it wants, one line
	// Command is the shell command an approval asks to run, "" for none.
	Command string `json:"command,omitempty"`
	// Answers are the labels proto.Press takes for a question or a plan,
	// as the pane's screen offers them; none means answer it in its tab.
	Answers []string `json:"answers,omitempty"`
	Form    string   `json:"form,omitempty"` // the form's id
}

// Inbox is everything that waits on the user across a daemon's tabs, the
// answers first and oldest first, and how many agents work meanwhile.
type Inbox struct {
	Items  []InboxItem
	Moving int
}

// Recommended is label without a trailing "(Recommended)", and whether it
// had one: the answer an agent marked as its pick.
func Recommended(label string) (string, bool) {
	s := strings.TrimSpace(label)
	if t, ok := strings.CutSuffix(strings.ToLower(s), "(recommended)"); ok {
		return strings.TrimSpace(s[:len(t)]), true
	}
	return s, false
}

// InboxHead is an inbox header's counts: "2 for you", "1 blocked",
// "3 moving", each left out at zero.
func InboxHead(items []InboxItem, moving int) []string {
	you, blocked := 0, 0
	for _, it := range items {
		if it.Kind.ForYou() {
			you++
		} else {
			blocked++
		}
	}
	var out []string
	if you > 0 {
		out = append(out, strconv.Itoa(you)+" for you")
	}
	if blocked > 0 {
		out = append(out, strconv.Itoa(blocked)+" blocked")
	}
	if moving > 0 {
		out = append(out, strconv.Itoa(moving)+" moving")
	}
	return out
}
