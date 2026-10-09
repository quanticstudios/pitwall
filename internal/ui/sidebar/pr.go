package sidebar

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// lucide archive, external-link, git-merge and rotate-ccw
const (
	icArchive  = "M3 3h18a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1ZM4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8M10 12h4"
	icExternal = "M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"
	icGitMerge = "M15 18a3 3 0 1 0 6 0a3 3 0 1 0-6 0M3 6a3 3 0 1 0 6 0a3 3 0 1 0-6 0M6 21V9a9 9 0 0 0 9 9"
	icRerun    = "M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8M3 3v5h5"
)

// PRColor is a pull request's state color, as GitHub shows it: a draft
// muted, open green, merged purple, closed red.
func PRColor(th *theme.Theme, pr model.PR) color.NRGBA {
	switch {
	case pr.State == model.PRMerged:
		return th.Purple
	case pr.State == model.PRClosed:
		return th.Red
	case pr.Draft:
		return th.Muted
	}
	return th.Green
}

// CheckColor is a CI state's dot color: pending yellow, pass green, fail
// red, skipped muted.
func CheckColor(th *theme.Theme, s model.CheckState) color.NRGBA {
	switch s {
	case model.CheckPending:
		return th.Yellow
	case model.CheckPass:
		return th.Green
	case model.CheckFail:
		return th.Red
	}
	return th.Muted
}

// PRSummary is a pull request in words: "#123 Open · Approved".
func PRSummary(pr model.PR) string {
	state := "Open"
	switch {
	case pr.State == model.PRMerged:
		state = "Merged"
	case pr.State == model.PRClosed:
		state = "Closed"
	case pr.Draft:
		state = "Draft"
	}
	s := fmt.Sprintf("#%d %s", pr.Number, state)
	if pr.State != model.PROpen {
		return s
	}
	switch pr.Review {
	case model.ReviewApproved:
		s += " · Approved"
	case model.ReviewChanges:
		s += " · Changes requested"
	case model.ReviewRequired:
		s += " · Review required"
	}
	if pr.Conflicts {
		s += " · Conflicts"
	}
	return s
}

// checkText is a check's state in the hover card.
func checkText(s model.CheckState) string {
	switch s {
	case model.CheckPending:
		return "running"
	case model.CheckPass:
		return "passed"
	case model.CheckFail:
		return "failed"
	}
	return "skipped"
}

// PRChip draws a tab's pull request on base: #123 on a pill in its
// PRColor; while it is open, the CI rollup's dot before the number and the
// review after it, a check when approved, a diff glyph in red when
// changes are requested.
func PRChip(gtx layout.Context, th *theme.Theme, pr model.PR, base color.NRGBA) layout.Dimensions {
	col := PRColor(th, pr)
	bg := theme.Mix(base, col, 0.14)
	h := gtx.Dp(16.5) // a pill's height
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max.Y = h
	off := op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops)
	var items []item
	open := pr.State == model.PROpen
	if ci := pr.CI(); open && ci != model.CheckNone {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			d := gtx.Dp(6)
			paint.FillShape(gtx.Ops, CheckColor(th, ci), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(d, d)}
		}})
	}
	items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Readable(col, bg), fmt.Sprintf("#%d", pr.Number))
	}})
	switch {
	case open && pr.Review == model.ReviewApproved:
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icCheck, gtx.Dp(10), th.Green, 0) }})
	case open && pr.Review == model.ReviewChanges:
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icFileDiff, gtx.Dp(10), th.Red, 0) }})
	}
	d := hrowFit(gtx, h, gtx.Dp(4), items...)
	off.Pop()
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(12), h)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: size}, h/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}

// PRBlocked says why tab ws cannot open, merge or re-run the failed checks
// of its pull request, "" when it can, in a few words for the menu's right
// edge. gh is whether the gh CLI is installed.
func PRBlocked(st *model.State, ws model.Workspace, gh bool) (open, merge, rerun string) {
	pr, ok := st.PRs[ws.ID]
	if !ok {
		return "no PR", "no PR", "no PR"
	}
	if pr.URL == "" {
		open = "no link"
	}
	switch {
	case !gh:
		merge = "no gh CLI"
	case pr.State != model.PROpen:
		merge = string(pr.State)
	case pr.Draft:
		merge = "draft"
	}
	switch {
	case !gh:
		rerun = "no gh CLI"
	case pr.CI() != model.CheckFail:
		rerun = "none failed"
	}
	return open, merge, rerun
}

// maxChecks is how many checks the hover card lists.
const maxChecks = 8

// prEntries are tab ws's menu entries for its pull request pr, in place of
// Create pull request: Open PR, then Merge PR and, when checks failed,
// Re-run failed checks while it is open, or Archive once it merged.
func (s *Sidebar) prEntries(v *view, ws model.Workspace, pr model.PR) []menuEntry {
	open, merge, rerun := PRBlocked(v.st, ws, s.GH)
	out := []menuEntry{{c: &s.menuItem[actOpenPR], icon: icExternal, text: fmt.Sprintf("Open PR #%d", pr.Number), off: open != "", hint: open}}
	if pr.State == model.PRMerged {
		return append(out, menuEntry{c: &s.menuItem[actArchive], icon: icArchive, text: "Archive…"})
	}
	out = append(out, menuEntry{c: &s.menuItem[actMergePR], icon: icGitMerge, text: "Merge PR…", off: merge != "", hint: merge})
	if pr.CI() == model.CheckFail {
		out = append(out, menuEntry{c: &s.menuItem[actRerun], icon: icRerun, text: "Re-run failed checks", off: rerun != "", hint: rerun})
	}
	return out
}

// prClicks turns this frame's clicks on tab ws's PR chip, Archive button
// and PR menu entries into events. An off entry stays open and does
// nothing.
func (s *Sidebar) prClicks(gtx layout.Context, v *view, ws model.Workspace) {
	r := s.row(ws.ID)
	for r.pr.Clicked(gtx) {
		s.events = append(s.events, OpenPR{WorkspaceID: ws.ID})
	}
	for r.archive.Clicked(gtx) {
		s.events = append(s.events, Archive{WorkspaceID: ws.ID})
	}
	if s.menuWS != ws.ID {
		return
	}
	open, merge, rerun := PRBlocked(v.st, ws, s.GH)
	var e Event
	switch {
	case s.menuItem[actOpenPR].Clicked(gtx) && open == "":
		e = OpenPR{WorkspaceID: ws.ID}
	case s.menuItem[actMergePR].Clicked(gtx) && merge == "":
		e = MergePR{WorkspaceID: ws.ID}
	case s.menuItem[actRerun].Clicked(gtx) && rerun == "":
		e = RerunChecks{WorkspaceID: ws.ID}
	case s.menuItem[actArchive].Clicked(gtx):
		e = Archive{WorkspaceID: ws.ID}
	}
	if e != nil {
		s.events = append(s.events, e)
		s.closeMenus()
	}
}
