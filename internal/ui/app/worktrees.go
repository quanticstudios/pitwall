package app

import (
	"image"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Worktreer is optionally implemented by a Backend: the daemon's reply to
// the last proto.WorktreeQuery, the zero value until it comes.
type Worktreer interface {
	Worktree() proto.WorktreeInfo
}

// worktreeForm is the state of the New worktree tab and Clean up
// worktrees dialogs.
type worktreeForm struct {
	kind      model.WorktreeKind
	name, ref widget.Editor
	filled    bool // ref took the default branch from the reply
	kinds     [4]widget.Clickable
	picks     [maxPicks]widget.Clickable
	err       string

	open, del []widget.Clickable // per orphan
	armed     string             // the orphan with changes whose Delete asks again, by path
}

// maxPicks is how many matching branches the New worktree tab dialog lists.
const maxPicks = 6

var kindLabels = [...]string{model.FromNew: "New branch", model.FromBranch: "Branch", model.FromRemote: "Remote branch", model.FromPR: "Pull request"}

// canWorktree reports whether the daemon answers proto.WorktreeQuery.
func (u *ui) canWorktree() bool {
	_, ok := u.b.(Worktreer)
	return ok && u.link().Level >= proto.Since(proto.WorktreeQuery{})
}

// worktreeReply is the daemon's answer to q, once it came.
func (u *ui) worktreeReply(q proto.WorktreeQuery) (proto.WorktreeInfo, bool) {
	if w, ok := u.b.(Worktreer); ok {
		if r := w.Worktree(); r.Query == q {
			return r, true
		}
	}
	return proto.WorktreeInfo{}, false
}

// openNewWorktree asks what group's new worktree starts from. A daemon
// that cannot answer makes a new branch off the default one right away.
func (u *ui) openNewWorktree(st *model.State, group string) {
	if !u.canWorktree() {
		u.nav.expectSession(st)
		u.send(proto.NewWorkspace{ProjectID: group})
		return
	}
	u.modal.open(modalNewWorktree, group)
	f := &u.modal.wt
	f.kind, f.err, f.filled = model.FromNew, "", false
	for _, e := range []*widget.Editor{&f.name, &f.ref} {
		e.SingleLine, e.Submit = true, true
		e.SetText("")
	}
	u.send(proto.WorktreeQuery{ProjectID: group})
}

// openDelete opens the delete dialog and, for a worktree pitwall made,
// asks what deleting it would lose.
func (u *ui) openDelete(st *model.State, id string) {
	u.modal.open(modalDelete, id)
	if ws := findWorkspace(st, id); ws != nil && ws.WorktreeRoot != "" && u.canWorktree() {
		u.send(proto.WorktreeQuery{WorkspaceID: id})
	}
}

// openCleanup opens Clean up worktrees; the daemon prunes, then lists.
func (u *ui) openCleanup() {
	if !u.canWorktree() {
		return
	}
	u.modal.open(modalCleanup, "")
	u.modal.wt.armed = ""
	u.send(proto.WorktreeQuery{Orphans: true})
}

// setKind switches the New worktree tab dialog to k and focuses its field.
func (u *ui) setKind(k model.WorktreeKind) {
	f := &u.modal.wt
	f.kind, f.err = k, ""
	ref := ""
	if r, ok := u.worktreeReply(proto.WorktreeQuery{ProjectID: u.modal.ws}); ok && k == model.FromNew {
		ref = r.Default
	}
	f.ref.SetText(ref)
	n := len([]rune(ref))
	f.ref.SetCaret(n, n)
	u.modal.focus = true
}

// worktreeFocus is the field that takes key focus when the dialog opens.
func (f *worktreeForm) worktreeFocus() *widget.Editor {
	if f.kind == model.FromNew {
		return &f.name
	}
	return &f.ref
}

// candidates is what ref picks from for kind.
func candidates(r proto.WorktreeInfo, kind model.WorktreeKind) []string {
	switch kind {
	case model.FromNew:
		return append(slices.Clone(r.Local), r.Remote...)
	case model.FromBranch:
		return r.Local
	case model.FromRemote:
		return r.Remote
	}
	return nil
}

// refMatches is up to maxPicks of the candidates holding text, ignoring
// case, the ones starting with it first; none once text names one. The
// New worktree tab and New task dialogs both offer branches with it.
func refMatches(cands []string, text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if slices.ContainsFunc(cands, func(c string) bool { return strings.ToLower(c) == text }) {
		return nil
	}
	var first, rest []string
	for _, c := range cands {
		switch l := strings.ToLower(c); {
		case strings.HasPrefix(l, text):
			first = append(first, c)
		case strings.Contains(l, text):
			rest = append(rest, c)
		}
	}
	out := append(first, rest...)
	return out[:min(len(out), maxPicks)]
}

// worktreeEvents handles the New worktree tab dialog's fields and buttons
// and reports whether Enter asked to confirm.
func (u *ui) worktreeEvents(gtx gl.Context) (confirm bool) {
	m, f := &u.modal, &u.modal.wt
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &f.name, Name: key.NameEscape},
			key.Filter{Focus: &f.name, Name: key.NameTab},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				m.close()
				return false
			}
			gtx.Execute(key.FocusCmd{Tag: &f.ref})
		}
	}
	r, _ := u.worktreeReply(proto.WorktreeQuery{ProjectID: m.ws})
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &f.ref, Name: key.NameEscape},
			key.Filter{Focus: &f.ref, Name: key.NameTab},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				m.close()
				return false
			}
			if ms := refMatches(candidates(r, f.kind), f.ref.Text()); len(ms) == 1 {
				f.ref.SetText(ms[0])
				n := len([]rune(ms[0]))
				f.ref.SetCaret(n, n)
			}
		}
	}
	for _, e := range []*widget.Editor{&f.name, &f.ref} {
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			switch ev.(type) {
			case widget.SubmitEvent:
				confirm = true
			case widget.ChangeEvent:
				f.err = ""
			}
		}
	}
	for i := range f.kinds {
		for f.kinds[i].Clicked(gtx) {
			u.setKind(model.WorktreeKind(i))
		}
	}
	for i, pick := range refMatches(candidates(r, f.kind), f.ref.Text()) {
		for f.picks[i].Clicked(gtx) {
			f.ref.SetText(pick)
			n := len([]rune(pick))
			f.ref.SetCaret(n, n)
			f.err = ""
		}
	}
	return confirm
}

// confirmNewWorktree checks the dialog's choice against the branches the
// daemon listed and asks for the tab.
func (u *ui) confirmNewWorktree(st *model.State) {
	m, f := &u.modal, &u.modal.wt
	r, have := u.worktreeReply(proto.WorktreeQuery{ProjectID: m.ws})
	from := model.WorktreeFrom{Kind: f.kind, Ref: strings.TrimSpace(f.ref.Text())}
	known := func(list []string) bool { return !have || slices.Contains(list, from.Ref) }
	switch f.kind {
	case model.FromNew:
		if from.Ref != "" && !known(candidates(r, f.kind)) {
			f.err = "No branch " + from.Ref + "."
		}
	case model.FromBranch, model.FromRemote:
		if from.Ref == "" || !known(candidates(r, f.kind)) {
			f.err = "Pick a branch from the list."
		}
	case model.FromPR:
		n, err := strconv.Atoi(strings.TrimPrefix(from.Ref, "#"))
		if err != nil || n <= 0 {
			f.err = "Type the pull request's number."
		}
		from = model.WorktreeFrom{Kind: model.FromPR, PR: n}
	}
	if f.err != "" {
		return
	}
	u.nav.expectSession(st)
	u.send(proto.NewWorkspace{ProjectID: m.ws, Name: strings.TrimSpace(f.name.Text()), From: from})
	m.close()
}

func (u *ui) newWorktreeBody(gtx gl.Context, st *model.State) gl.Dimensions {
	th, m, f := u.th, &u.modal, &u.modal.wt
	r, have := u.worktreeReply(proto.WorktreeQuery{ProjectID: m.ws})
	if have && !f.filled {
		f.filled = true
		if f.kind == model.FromNew && f.ref.Text() == "" {
			f.ref.SetText(r.Default)
		}
	}
	group := "the group"
	if i := slices.IndexFunc(st.Projects, func(p model.Project) bool { return p.ID == m.ws }); i >= 0 {
		group = st.Projects[i].Name
	}
	text := func(c gl.Widget) gl.FlexChild { return gl.Rigid(c) }
	label := func(s string) gl.FlexChild {
		return text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, medium(th.UIFont), 13, th.Fg, s) })
	}
	note := func(s string) gl.FlexChild {
		return text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, th.Muted, s) })
	}
	kids := []gl.FlexChild{
		text(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "New worktree tab in "+group)
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		text(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 14, th.Muted, "The tab gets its own folder under .worktrees, checked out from:")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		text(func(gtx gl.Context) gl.Dimensions { return u.kindTabs(gtx, r.GitHub) }),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
	}
	var refLabel, refNote string
	switch f.kind {
	case model.FromNew:
		kids = append(kids, label("Name"), gl.Rigid(gl.Spacer{Height: 6}.Layout),
			text(func(gtx gl.Context) gl.Dimensions { return u.field(gtx, &f.name, th.MonoFont, gtx.Dp(36), "") }),
			gl.Rigid(gl.Spacer{Height: 4}.Layout), note("Also the new branch's name. Empty picks one."),
			gl.Rigid(gl.Spacer{Height: 12}.Layout))
		refLabel, refNote = "Base", "A local or remote branch. Tab completes."
	case model.FromBranch:
		refLabel, refNote = "Local branch", "The tab is named after it."
	case model.FromRemote:
		refLabel, refNote = "Remote branch", "A local branch of the same name tracks it."
	case model.FromPR:
		refLabel, refNote = "Pull request number", "Fetched from origin into the branch pr-<number>."
	}
	kids = append(kids, label(refLabel), gl.Rigid(gl.Spacer{Height: 6}.Layout),
		text(func(gtx gl.Context) gl.Dimensions { return u.field(gtx, &f.ref, th.MonoFont, gtx.Dp(36), "") }),
		gl.Rigid(gl.Spacer{Height: 4}.Layout))
	switch {
	case f.err != "":
		kids = append(kids, text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, th.Red, f.err) }))
	case have && r.Err != "":
		kids = append(kids, text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, th.Red, r.Err) }))
	case !have && f.kind != model.FromPR:
		kids = append(kids, note("Reading the branches…"))
	default:
		kids = append(kids, note(refNote))
	}
	if picks := refMatches(candidates(r, f.kind), f.ref.Text()); len(picks) > 0 {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 6}.Layout))
		for i, p := range picks {
			kids = append(kids, text(func(gtx gl.Context) gl.Dimensions { return u.pickRow(gtx, &f.picks[i], p) }))
		}
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 20}.Layout),
		text(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Create", th.Primary, th.OnPrimary)
		}),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// kindTabs is the row of what a worktree starts from, the chosen one
// filled; Pull request only for a GitHub origin.
func (u *ui) kindTabs(gtx gl.Context, github bool) gl.Dimensions {
	th, f := u.th, &u.modal.wt
	h, x := gtx.Dp(28), 0
	for i, l := range kindLabels {
		if model.WorktreeKind(i) == model.FromPR && !github {
			continue
		}
		on := model.WorktreeKind(i) == f.kind
		fg, bg := th.Fg, th.SurfaceSecondary
		if on {
			fg, bg = th.OnPrimary, th.Primary
		}
		call, sz := textCall(gtx, th, medium(th.UIFont), 13, fg, l)
		w := sz.X + 2*gtx.Dp(12)
		o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w, h))
		f.kinds[i].Layout(g, func(gtx gl.Context) gl.Dimensions {
			b := bg
			if f.kinds[i].Hovered() && !on {
				b = theme.Mix(bg, th.Fg, 0.08)
			}
			paint.FillShape(gtx.Ops, b, clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Op(gtx.Ops))
			pointer.CursorPointer.Add(gtx.Ops)
			t := op.Offset(image.Pt((w-sz.X)/2, (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			t.Pop()
			return gl.Dimensions{Size: image.Pt(w, h)}
		})
		o.Pop()
		x += w + gtx.Dp(6)
	}
	return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// pickRow is one matching branch under the ref field; a click takes it.
func (u *ui) pickRow(gtx gl.Context, c *widget.Clickable, name string) gl.Dimensions {
	th := u.th
	return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		h := gtx.Dp(24)
		size := image.Pt(gtx.Constraints.Max.X, h)
		if c.Hovered() {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.06), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
		}
		call, sz := textCall(gtx, th, th.MonoFont, 12, theme.Mix(th.Surface, th.Fg, 0.8), name)
		o := op.Offset(image.Pt(gtx.Dp(8), (h-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
}

// deleteForce reports whether deleting ws needs Force: the daemon found
// changes, or an unmerged branch the user chose to delete.
func (u *ui) deleteForce(ws string) bool {
	r, ok := u.worktreeReply(proto.WorktreeQuery{WorkspaceID: ws})
	return ok && r.Err == "" && (len(r.Changed) > 0 || u.modal.removeBranch && r.Unmerged)
}

// maxChanged is how many changed files the delete dialog lists.
const maxChanged = 8

// deleteChanges is what the delete dialog says about the worktree's
// files, from the daemon's answer.
func (u *ui) deleteChanges(ws string) []gl.FlexChild {
	th := u.th
	note := func(c, s string) gl.FlexChild {
		col := th.Muted
		if c == "warn" {
			col = th.Yellow
		}
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, col, s) })
	}
	if !u.canWorktree() {
		return []gl.FlexChild{note("", "Git refuses if it has uncommitted files.")}
	}
	r, ok := u.worktreeReply(proto.WorktreeQuery{WorkspaceID: ws})
	switch {
	case !ok:
		return []gl.FlexChild{note("", "Checking for uncommitted files…")}
	case r.Err != "":
		return []gl.FlexChild{note("warn", r.Err)}
	case len(r.Changed) == 0:
		return []gl.FlexChild{note("", "It has no uncommitted files.")}
	}
	kids := []gl.FlexChild{gl.Rigid(gl.Spacer{Height: 6}.Layout), note("warn", "These uncommitted files are lost:")}
	for i, p := range r.Changed {
		if i == maxChanged {
			kids = append(kids, note("", "and "+strconv.Itoa(len(r.Changed)-i)+" more"))
			break
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.MonoFont, 12, theme.Mix(th.Surface, th.Fg, 0.8), p)
		}))
	}
	return kids
}

// branchNote is the second line under "Also delete the branch".
func (u *ui) branchNote(ws string) string {
	r, ok := u.worktreeReply(proto.WorktreeQuery{WorkspaceID: ws})
	switch {
	case !ok || r.Err != "":
		return "git branch -d refuses if it is not merged."
	case r.Unmerged:
		return "Not merged into the default branch: its commits are lost."
	}
	return "Merged into the default branch."
}

// maxOrphans is how many worktrees Clean up worktrees lists.
const maxOrphans = 8

func (u *ui) cleanupBody(gtx gl.Context, st *model.State) gl.Dimensions {
	th, f := u.th, &u.modal.wt
	r, have := u.worktreeReply(proto.WorktreeQuery{Orphans: true})
	text := func(c gl.Widget) gl.FlexChild { return gl.Rigid(c) }
	kids := []gl.FlexChild{
		text(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Clean up worktrees")
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
	}
	muted := func(s string) gl.FlexChild {
		return text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 14, th.Muted, s) })
	}
	switch {
	case !have:
		kids = append(kids, muted("Pruning, then looking for worktrees no tab uses…"))
	case len(r.Orphans) == 0 && r.Err == "":
		kids = append(kids, muted("Every worktree under .worktrees has a tab."))
	default:
		kids = append(kids, muted("No tab uses these worktrees under .worktrees. Open one in a tab, or delete it; its branch stays."))
	}
	if have && r.Err != "" {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout),
			text(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, th.Red, r.Err) }))
	}
	for len(f.open) < len(r.Orphans) {
		f.open, f.del = append(f.open, widget.Clickable{}), append(f.del, widget.Clickable{})
	}
	now := time.Now()
	for i, o := range r.Orphans {
		if i == maxOrphans {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 12}.Layout),
				muted("and "+strconv.Itoa(len(r.Orphans)-i)+" more: pitwall worktree prune lists them all."))
			break
		}
		for f.open[i].Clicked(gtx) {
			u.openOrphan(st, o)
			return gl.Dimensions{}
		}
		for f.del[i].Clicked(gtx) {
			u.deleteOrphan(o)
		}
		where := "detached in " + filepath.Base(o.Root)
		if o.Branch != "" {
			where = "branch " + o.Branch + " in " + filepath.Base(o.Root)
		}
		lines := []string{where}
		if !o.Committed.IsZero() {
			lines = append(lines, "last commit "+sidebar.RelTime(now, o.Committed))
		}
		if o.Dirty {
			lines = append(lines, "Uncommitted files, lost on delete")
		}
		del := "Delete"
		if f.armed == o.Path {
			del = "Delete anyway"
		}
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 16}.Layout), text(func(gtx gl.Context) gl.Dimensions {
			tg := gtx
			tg.Constraints = gl.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-gtx.Dp(196)), gtx.Constraints.Max.Y)}
			rows := []gl.FlexChild{gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, medium(th.UIFont), 14, th.Fg, filepath.Base(o.Path))
			})}
			for j, l := range lines {
				col := th.Muted
				if o.Dirty && j == len(lines)-1 {
					col = th.Yellow
				}
				rows = append(rows, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 12, col, l) }))
			}
			d := gl.Flex{Axis: gl.Vertical}.Layout(tg, rows...)
			b := u.buttonPair(gtx, &f.open[i], &f.del[i], "Open", del, th.Red, theme.Hex("#ffffff"))
			return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, max(d.Size.Y, b.Size.Y))}
		}))
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		text(func(gtx gl.Context) gl.Dimensions { return u.buttons(gtx, "", "Close", th.Primary, th.OnPrimary) }),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// deleteOrphan is o's Delete button. One with changes asks again first,
// its button then reading Delete anyway.
func (u *ui) deleteOrphan(o model.Orphan) {
	f := &u.modal.wt
	if o.Dirty && f.armed != o.Path {
		f.armed = o.Path
		return
	}
	f.armed = ""
	u.send(proto.DeleteWorktree{Root: o.Root, Path: o.Path, Force: o.Dirty})
	u.send(proto.WorktreeQuery{Orphans: true}) // the list again, without it
}

// openOrphan opens a tab in orphan o, in its repo's group of the window's
// session when there is one.
func (u *ui) openOrphan(st *model.State, o model.Orphan) {
	group := ""
	for _, p := range st.Projects {
		if p.SessionID == u.nav.session && p.Kind == model.ProjectGit && p.Root == o.Root {
			group = p.ID
		}
	}
	u.nav.expectSession(st)
	u.send(proto.NewSession{Cwd: o.Path, GroupID: group, SessionID: u.nav.session})
	u.modal.close()
}
