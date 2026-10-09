package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type menuEntry struct {
	c          *widget.Clickable
	icon, text string
	danger     bool   // red, like Delete
	off        bool   // muted and inert; hint says why
	sep        bool   // a divider above it
	sub        bool   // a chevron: hovering it opens a submenu
	hint       string // muted text at the right edge
}

// menu is WorkspaceOverflowMenu with tab words plus the grouping actions. Move, new group
// and remove act on the whole selection when ws is part of it.
func (s *Sidebar) menu(gtx layout.Context, v *view, ws model.Workspace, anchor image.Rectangle) {
	th := v.th
	s.catcher(gtx)
	targets := s.targets(v, ws.ID)
	cur := v.groupOf(ws.ID)
	var groups []model.Project
	grouped := false
	for _, id := range targets {
		if g := v.groupOf(id); g != "" {
			grouped = true
		}
	}
	for _, p := range v.st.Projects {
		if p.ID != cur || len(targets) > 1 {
			groups = append(groups, p)
		}
	}
	newText := "New group"
	if len(targets) > 1 {
		newText = fmt.Sprintf("New group from %d tabs", len(targets))
	}
	entries := []menuEntry{{c: &s.menuItem[actNewBelow], icon: icPlus, text: "New tab below"}, {c: &s.menuItem[actRename], icon: icPencil, text: "Rename tab"}}
	move := -1
	if len(groups) > 0 {
		move = len(entries)
		entries = append(entries, menuEntry{c: &s.menuItem[actMove], icon: icFolderInput, text: "Move to group", sub: true})
	}
	entries = append(entries, menuEntry{c: &s.menuItem[actNewGroup], icon: icFolderPlus, text: newText})
	if grouped {
		entries = append(entries, menuEntry{c: &s.menuItem[actUngroup], icon: icFolderMinus, text: "Remove from group"})
	}
	if n := folderCount(v.st, ws); n > 0 {
		entries = append(entries, menuEntry{c: &s.menuItem[actGroupFolder], icon: projectIcon("folder"),
			text: "Group tabs in " + baseName(ws.RepoRoot), hint: fmt.Sprint(n)})
	}
	if _, git := v.st.Stats[ws.ID]; git {
		diff, pr := ReviewBlocked(v.st, ws, s.GH)
		entries = append(entries, menuEntry{c: &s.menuItem[actDiff], icon: icFileDiff, text: "View diff", sep: true, off: diff != "", hint: diff})
		if p, ok := v.st.PRs[ws.ID]; ok && p.State != model.PRClosed {
			entries = append(entries, s.prEntries(v, ws, p)...)
		} else {
			entries = append(entries, menuEntry{c: &s.menuItem[actPR], icon: icGitPullRequest, text: "Create pull request", off: pr != "", hint: pr})
		}
	}
	entries = append(entries,
		menuEntry{c: &s.menuItem[actDetach], icon: icDetach, text: "Detach tab", sep: true},
		menuEntry{c: &s.menuItem[actClose], icon: icX, text: "Close tab"})
	if ws.WorktreeRoot != "" { // only a worktree pitwall made has a folder to delete
		entries = append(entries, menuEntry{c: &s.menuItem[actDelete], icon: icTrash, text: "Delete…", danger: true})
	}
	// Hovering "Move to group" opens its flyout; hovering another entry
	// closes it.
	was := s.moveOpen
	for i, e := range entries {
		if e.c.Hovered() {
			s.moveOpen = i == move
		}
	}
	if s.moveOpen && !was {
		s.subAt = gtx.Now
	}
	at, tops := s.menuList(gtx, th, anchor, entries)
	if move < 0 {
		return
	}
	w := gtx.Dp(220)
	if !s.moveOpen {
		return
	}
	for id := range s.moveBtn {
		if !slices.ContainsFunc(groups, func(p model.Project) bool { return p.ID == id }) {
			delete(s.moveBtn, id)
		}
	}
	sw, itemH, p := gtx.Dp(200), gtx.Dp(32), gtx.Dp(4)
	size := image.Pt(sw, 2*p+len(groups)*itemH)
	// Beside the menu, level with its entry.
	row := image.Rectangle{Min: at.Add(image.Pt(0, tops[move]-p)), Max: at.Add(image.Pt(w, tops[move]-p+itemH))}
	sub := kit.Place(row.Add(anchor.Min), size, s.bounds(), kit.Beside, gtx.Dp(2), gtx.Dp(8)).Sub(anchor.Min)
	defer op.Offset(sub).Push(gtx.Ops).Pop()
	defer s.popIn(gtx, s.subAt, size)()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)
	fly := make([]*widget.Clickable, len(groups))
	defer s.noteFlyout(fly)
	for i, g := range groups {
		c := s.moveBtn[g.ID]
		if c == nil {
			c = &widget.Clickable{}
			s.moveBtn[g.ID] = c
		}
		fly[i] = c
		s.menuRow(gtx, th, c, image.Pt(p, p+i*itemH), image.Pt(sw-2*p, itemH), projectIcon(g.Icon), th.ProjectColor(g.Color), g.Name, th.Fg, "", false)
	}
}

// menuList draws entries as a menu placed "bottom end" under anchor, the
// trigger's rect in the sidebar, which the drawing's origin is the top left
// of. It flips above when there is no room below. It returns the menu's
// top left and each entry's top inside it.
func (s *Sidebar) menuList(gtx layout.Context, th *theme.Theme, anchor image.Rectangle, entries []menuEntry) (image.Point, []int) {
	w, itemH, p, sepH := gtx.Dp(220), gtx.Dp(32), gtx.Dp(4), gtx.Dp(9)
	tops := make([]int, len(entries))
	y := p
	for i, e := range entries {
		if e.sep {
			y += sepH
		}
		tops[i] = y
		y += itemH
	}
	s.noteMenu(entries)
	size := image.Pt(w, y+p)
	at := kit.Place(anchor, size, s.bounds(), kit.BelowEnd, gtx.Dp(4), gtx.Dp(8)).Sub(anchor.Min)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	defer s.popIn(gtx, s.menuAt, size)()
	floatingSurface(gtx, th, size)

	s.blockClicks(gtx, size)
	for i, e := range entries {
		if e.sep {
			sy := tops[i] - sepH/2 - 1
			paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Border, 0.9), clip.Rect{Min: image.Pt(p+gtx.Dp(4), sy), Max: image.Pt(w-p-gtx.Dp(4), sy+1)}.Op())
		}
		col, iconCol := th.Fg, th.Muted
		switch {
		case e.danger:
			col, iconCol = th.Red, th.Red
		case e.off:
			col, iconCol = th.Muted, theme.Mix(th.SurfaceSecondary, th.Muted, 0.6)
		}
		s.menuRow(gtx, th, e.c, image.Pt(p, tops[i]), image.Pt(w-2*p, itemH), e.icon, iconCol, e.text, col, e.hint, e.off)
		if e.sub {
			off := op.Offset(image.Pt(w-gtx.Dp(28), tops[i]+(itemH-gtx.Dp(14))/2)).Push(gtx.Ops)
			drawIcon(gtx, icChevronRight, gtx.Dp(14), th.Muted, 0)
			off.Pop()
		}

	}
	return at, tops
}

// menuRow draws one entry; an off one has no hover fill.
func (s *Sidebar) menuRow(gtx layout.Context, th *theme.Theme, c *widget.Clickable, at, size image.Point, icon string, iconCol color.NRGBA, text string, col color.NRGBA, hint string, off bool) {
	defer op.Offset(at).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(size)
	clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if (c.Hovered() || s.keyed(c)) && !off {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		if gtx.Focused(c) {
			g := gtx.Dp(3)
			kit.FocusRing(gtx, th, image.Rectangle{Max: size}.Inset(g), gtx.Dp(8)-g)
		}
		gtx.Constraints = layout.Exact(image.Pt(size.X-gtx.Dp(16), size.Y))
		defer op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops).Pop()
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icon, gtx.Dp(14), iconCol, 0) }},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Body), col, text)
			}},
		}
		if hint != "" {
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, hint)
			}})
		}
		hrow(gtx, size.Y, gtx.Dp(8), items...)
		return layout.Dimensions{Size: size}
	})
}

// ReviewBlocked says why tab ws cannot show its diff, or open a pull
// request, "" when it can, in a few words for the menu's right edge. gh is
// whether the gh CLI is installed.
func ReviewBlocked(st *model.State, ws model.Workspace, gh bool) (diff, pr string) {
	bs := st.Stats[ws.ID]
	switch {
	case bs.Base == "" && bs.MergeStatus != "":
		diff = "restart daemon" // one before proto.Level 2 sends no Base
	case bs.Base == "":
		diff = "no base branch"
	}
	base := gitstat.BranchName(bs.Base)
	switch {
	case diff != "":
		pr = diff
	case !gh:
		pr = "no gh CLI"
	case ws.Branch == "":
		pr = "no branch"
	case ws.Branch == base:
		pr = "on " + base
	case bs.Ahead == 0:
		pr = "no commits"
	}
	return diff, pr
}

// catcher covers the whole window under a popover so a click anywhere else
// closes it.
func (s *Sidebar) catcher(gtx layout.Context) {
	const far = 1 << 16
	defer clip.Rect{Min: image.Pt(-far, -far), Max: image.Pt(far, far)}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.dismiss)
}

// blockClicks keeps a click on a popover's body, between its buttons, from
// reaching the catcher underneath and closing it.
func (s *Sidebar) blockClicks(gtx layout.Context, size image.Point) {
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.popover)
}

// appearanceOf is p's icon and color with aide's defaults filled in.
func appearanceOf(p model.Project) (icon, color string) {
	icon, color = p.Icon, p.Color
	if icon == "" {
		icon = "folder"
	}
	if color == "" {
		color = "neutral"
	}
	return icon, color
}

// appearanceMenu is ProjectOverflowMenu without the icon search and the
// setup entry: a 7-column icon grid and aide's color row, placed under
// anchor, the trigger's rect in the sidebar, kept inside the window.
func (s *Sidebar) appearanceMenu(gtx layout.Context, th *theme.Theme, p model.Project, anchor image.Rectangle) {
	s.catcher(gtx)
	icon, col := appearanceOf(p)
	w, pad, gap := gtx.Dp(320), gtx.Dp(10), gtx.Dp(4)
	inner := w - 2*pad
	cell, sw := gtx.Dp(32), gtx.Dp(24)
	const cols = 7
	rows := (len(projectIcons) + cols - 1) / cols
	perRow := max(1, (inner+gap)/(sw+gap))
	colorRows := (len(projectColorIDs) + perRow - 1) / perRow
	labelH := gtx.Dp(16)
	gridH := rows*(cell+gap) - gap
	colorH := colorRows*(sw+gap) - gap
	size := image.Pt(w, pad+labelH+gtx.Dp(8)+gridH+gtx.Dp(12)+labelH+gtx.Dp(8)+colorH+pad)

	at := kit.Place(anchor, size, s.bounds(), kit.BelowEnd, gtx.Dp(4), gtx.Dp(8)).Sub(anchor.Min)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	defer s.popIn(gtx, s.menuAt, size)()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)

	section := func(y int, text string) {
		off := op.Offset(image.Pt(pad+gtx.Dp(4), y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(inner, labelH))
		hrow(g, labelH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), th.Muted, text)
		}})
		off.Pop()
	}
	y := pad
	section(y, "Icon")
	y += labelH + gtx.Dp(8)
	colW := (inner - (cols-1)*gap) / cols
	for i, ic := range projectIcons {
		c := &s.iconBtn[i]
		at := image.Pt(pad+(i%cols)*(colW+gap), y+(i/cols)*(cell+gap))
		selected := ic.name == icon
		off := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(cell, cell))
		clickable(g, c, func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(0, 0, cell, cell)
			fg := theme.Mix(th.SurfaceSecondary, th.Fg, 0.85)
			switch {
			case selected:
				bg := th.SelectedBg
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
				paint.FillShape(gtx.Ops, theme.Mix(bg, th.Primary, 0.5), clip.Stroke{Path: clip.UniformRRect(r, gtx.Dp(8)).Path(gtx.Ops), Width: 1}.Op())
				fg = th.Primary
			case c.Hovered():
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
				fg = th.Fg
			}
			return drawCentered(gtx, cell, func(gtx layout.Context) layout.Dimensions {
				return drawIcon(gtx, ic.d, gtx.Dp(14), fg, 0)
			})
		})
		off.Pop()
	}
	y += gridH + gtx.Dp(12)
	section(y, "Color")
	y += labelH + gtx.Dp(8)
	for i, id := range projectColorIDs {
		c := &s.colorBtn[i]
		at := image.Pt(pad+(i%perRow)*(sw+gap), y+(i/perRow)*(sw+gap))
		off := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(sw, sw))
		clickable(g, c, func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(0, 0, sw, sw)
			if c.Hovered() {
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.Ellipse(r).Op(gtx.Ops))
			}
			if id == col {
				paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.7), clip.Stroke{Path: clip.Ellipse(r).Path(gtx.Ops), Width: 1}.Op())
			}
			d := gtx.Dp(16)
			o := (sw - d) / 2
			paint.FillShape(gtx.Ops, th.ProjectColor(id), clip.Ellipse(image.Rect(o, o, o+d, o+d)).Op(gtx.Ops))
			return layout.Dimensions{Size: r.Size()}
		})
		off.Pop()
	}
}

// floatingSurface is aide's .floating-surface: popover fill, a 1px
// border-strong ring and an inset top highlight, rounded-lg.
func floatingSurface(gtx layout.Context, th *theme.Theme, size image.Point) {
	r := gtx.Dp(theme.RadiusPopover)
	rect := image.Rectangle{Max: size}
	kit.Surface(gtx, rect.Inset(-1), r+1, kit.Floating, th.BorderStrong, th.SurfaceSecondary)
	hl := clip.UniformRRect(rect, r).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.06), clip.Rect{Min: image.Pt(r, 0), Max: image.Pt(size.X-r, 1)}.Op())
	hl.Pop()
}

// bounds is the window in sidebar coordinates, for placing popovers.
func (s *Sidebar) bounds() image.Rectangle {
	w := s.Window.X
	if w == 0 {
		w = 1 << 16
	}
	return image.Rect(0, 0, w, s.height)
}

// popIn eases a popover of size at the drawing's origin in from at.
func (s *Sidebar) popIn(gtx layout.Context, at time.Time, size image.Point) func() {
	return kit.PopIn(gtx, anim.At(gtx, at, anim.Menu), image.Rectangle{Max: size}, -4)
}

// noteMenus starts a menu's entrance when the open one changes.
func (s *Sidebar) noteMenus(now time.Time) {
	k := fmt.Sprint(s.menuWS, "\x00", s.groupMenu, "\x00", s.appearance, "\x00", s.detachedOpen)
	if k != s.menuKey {
		s.menuKey, s.menuAt = k, now
	}
}

// folderCount is how many ungrouped tabs, detached ones included, share

// ws's RepoRoot: the ones "Group tabs in" would move.
func folderCount(st *model.State, ws model.Workspace) int {
	if ws.RepoRoot == "" {
		return 0
	}
	groups := map[string]bool{}
	for _, p := range st.Projects {
		groups[p.ID] = true
	}
	n := 0
	for _, w := range st.Workspaces {
		if w.RepoRoot == ws.RepoRoot && !groups[w.ProjectID] {
			n++
		}
	}
	return n
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}
