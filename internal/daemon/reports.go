package daemon

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Report card limits: cards an orchestrator keeps, and the runes of a
// card's line and of the report it opens to.
const (
	maxReports    = 30
	reportLineCut = 240
	reportBodyCut = 1500
)

// prRef finds a pull request a report names: "PR #566", "PR 566", ".../pull/566".
var prRef = regexp.MustCompile(`(?i)\bPR #?(\d+)\b|/pull/(\d+)\b`)

// parseReport reads how a worker's final reply ended: its last non-empty
// line is DONE, BLOCKED or BLOCKED: <why>, with any markdown emphasis
// around it; anything else is in progress. line is the reason a blocked
// worker gave, else the report's first line; body is the report without
// the status line.
func parseReport(msg string) (status model.ReportStatus, line, body string) {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	last := strings.Trim(lines[len(lines)-1], " \t\r*_`>")
	status = model.ReportProgress
	why, blocked := strings.CutPrefix(last, "BLOCKED")
	why = strings.TrimSpace(why)
	switch {
	case strings.TrimSuffix(last, ".") == "DONE":
		status = model.ReportDone
	case blocked && (why == "" || strings.HasPrefix(why, ":")):
		status = model.ReportBlocked
		line = strings.Trim(strings.TrimSpace(strings.TrimPrefix(why, ":")), "*_`")
	}
	if status != model.ReportProgress {
		lines = lines[:len(lines)-1]
	}
	body = strings.TrimSpace(strings.Join(lines, "\n"))
	if line == "" {
		line = firstReportLine(body)
	}
	return status, cut(line, reportLineCut), cut(body, reportBodyCut)
}

// firstReportLine is body's first line with words in it, its markdown
// marks taken off.
func firstReportLine(body string) string {
	for l := range strings.SplitSeq(body, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>-*| "))
		l = strings.ReplaceAll(l, "**", "")
		if l != "" && !strings.HasPrefix(l, "```") && strings.Trim(l, "-=_*") != "" {
			return l
		}
	}
	return ""
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// noteReport posts the card of the turn pane p's agent just ended, its
// last message msg, into the chat of p's orchestrator: the tab that
// started p's tab. One card per turn: a turn already posted stays as it
// is. Callers hold d.mu, after keepReply counted the turn.
func (d *Daemon) noteReport(p model.Pane, msg string, at time.Time) {
	i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == p.WorkspaceID })
	if i < 0 || d.st.Workspaces[i].Parent == "" || d.st.Workspaces[i].RemoteParent {
		return
	}
	w := d.st.Workspaces[i]
	j := slices.IndexFunc(d.st.Workspaces, func(o model.Workspace) bool { return o.ID == w.Parent })
	if j < 0 {
		return
	}
	chat := reportChat(&d.st, d.st.Workspaces[j])
	if chat == "" {
		return
	}
	name := w.Name // the sidebar's title: a chosen name, else the label
	if !w.NameSet {
		name = cmp.Or(w.Label, w.Name)
	}
	r := model.Report{Tab: w.ID, Pane: p.ID, Name: name, PR: d.st.PRs[w.ID].Number, Turn: d.turnCount[p.ID], At: at}
	r.Status, r.Line, r.Body = parseReport(msg)
	if m := prRef.FindStringSubmatch(msg); r.PR == 0 && m != nil {
		r.PR, _ = strconv.Atoi(cmp.Or(m[1], m[2]))
	}
	old := d.st.Reports[chat]
	if slices.ContainsFunc(old, func(o model.Report) bool { return o.Key() == r.Key() }) {
		return
	}
	if d.st.Reports == nil {
		d.st.Reports = map[string][]model.Report{}
	}
	// Cards are replaced, never changed in place: clients hold the old slice.
	next := append(slices.Clone(old[max(0, len(old)-maxReports+1):]), r)
	d.st.Reports[chat] = next
	for id := range d.st.Reports { // an orchestrator that closed takes its cards along
		if !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }) &&
			!slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.Chat == id }) {
			delete(d.st.Reports, id)
		}
	}
}

// reportChat is the chat id of orchestrator tab o: its chat, else its
// first agent pane; "" for none.
func reportChat(st *model.State, o model.Workspace) string {
	if o.Chat != "" {
		return o.Chat
	}
	for _, ap := range st.AgentPanes(o) {
		if model.IsAgent(ap.Pane.Provider) {
			return ap.Pane.ID
		}
	}
	return ""
}
