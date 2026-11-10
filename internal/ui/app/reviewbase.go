package app

import (
	"context"
	"image"
	"log"
	"slices"
	"strings"
	"sync"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// readRefs, readFrom and readPRBase are what the base list reads; tests
// replace them.
var (
	readRefs   = gitstat.ListRefs
	readFrom   = gitstat.CreatedFrom
	readPRBase = forge.Base
)

// basePicker is the review header's list of bases the Branch scope can
// diff from, filtered by what is typed.
type basePicker struct {
	open, focusAsked bool
	anchor           image.Rectangle // the header's base button, in the view's pixels
	filter           widget.Editor
	sel              int // among the rows the filter shows
	btns             []widget.Clickable
	list             gl.List
	outside          int

	mu    sync.Mutex
	items []baseItem // nil while they are read
	err   string
}

// baseItem is one row of the base list: ref is what git diffs from, ""
// for the default branch; note says why the row is there.
type baseItem struct{ ref, label, note string }

// baseItems is the base list: the default branch, the branch the tab's
// worktree was made from, into, the branch its pull request merges into,
// then the other local branches, each branch once, and never branch, the
// tab's own.
func baseItems(refs gitstat.Refs, from, into, branch string) []baseItem {
	items := []baseItem{{label: refs.Default, note: "default branch"}}
	if refs.Default == "" {
		items[0].label = "default branch"
	}
	at := map[string]int{prBase(refs.Default): 0, branch: -1}
	add := func(ref, note string) {
		if ref == "" {
			return
		}
		if i, ok := at[prBase(ref)]; ok {
			if i >= 0 && note != "" && !strings.Contains(items[i].note, note) {
				items[i].note = strings.TrimPrefix(items[i].note+" · "+note, " · ")
			}
			return
		}
		at[prBase(ref)] = len(items)
		items = append(items, baseItem{ref, ref, note})
	}
	add(from, "created from")
	if into != "" {
		ref := into
		if remote := "origin/" + into; slices.Contains(refs.Remote, remote) {
			ref = remote
		}
		add(ref, "pull request base")
	}
	for _, b := range refs.Local {
		add(b, "")
	}
	return items
}

// shownBases is the indexes of the items whose label holds q, ignoring
// case; all of them for an empty q.
func shownBases(items []baseItem, q string) []int {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []int
	for i, it := range items {
		if strings.Contains(strings.ToLower(it.label), q) {
			out = append(out, i)
		}
	}
	return out
}

// openBases opens the base list and reads its rows in the background.
func (u *ui) openBases(st *model.State) {
	r := &u.review
	p := &r.bases
	ws := findWorkspace(st, r.ws)
	if ws == nil || r.staged() {
		return
	}
	if _, in, _ := r.turnState(); in {
		return
	}
	p.open, p.focusAsked, p.sel = true, false, 0
	p.filter.SingleLine, p.filter.Submit = true, true
	p.filter.SetText("")
	p.mu.Lock()
	p.items, p.err = nil, ""
	p.mu.Unlock()
	dir, branch, invalidate := r.dir, ws.Branch, u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	pr, hasPR := st.PRs[r.ws]
	gh := hasPR && ghInstalled()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), filesWait)
		defer cancel()
		refs, err := readRefs(ctx, dir)
		var base string
		if gh {
			var e error
			if base, e = readPRBase(ctx, dir, pr.Number); e != nil {
				log.Printf("review: base of PR #%d: %q", pr.Number, e)
			}
		}
		items := baseItems(refs, readFrom(ctx, dir), base, branch)
		p.mu.Lock()
		p.items = items
		if err != nil {
			p.err = err.Error()
		}
		p.mu.Unlock()
		invalidate()
	}()
}

// shownItems is the base list's rows and the indexes the filter shows.
func (p *basePicker) shownItems() ([]baseItem, []int) {
	p.mu.Lock()
	items := p.items
	p.mu.Unlock()
	return items, shownBases(items, p.filter.Text())
}

// pickBase makes ref, "" for the default branch, the review's base for
// the tab and saves that, and closes the list.
func (u *ui) pickBase(st *model.State, it baseItem) {
	r := &u.review
	r.bases.open, r.focus = false, true
	if u.nav.reviewBase == nil {
		u.nav.reviewBase = map[string]string{}
		u.gui.ReviewBase = u.nav.reviewBase
	}
	if it.ref == "" {
		delete(u.nav.reviewBase, r.ws)
	} else {
		u.nav.reviewBase[r.ws] = it.ref
	}
	for id := range u.nav.reviewBase { // a closed tab's pick goes too
		if findWorkspace(st, id) == nil {
			delete(u.nav.reviewBase, id)
		}
	}
	if u.report != nil { // a real window, not a test
		saveGUIState(u.gui)
	}
	if it.ref != r.picked() {
		r.setBase(it.ref)
		r.sel, r.selPath = 0, ""
	}
	r.status = "Diffing from " + it.label
}

// baseKeys takes the base list's keys while it is open: Escape closes it,
// the arrows move the selection, Enter picks it, and anything else types
// into the filter. It reports whether the list is open.
func (u *ui) baseKeys(gtx gl.Context, st *model.State) bool {
	r := &u.review
	p := &r.bases
	if !p.open {
		return false
	}
	items, shown := p.shownItems()
	pick := func() {
		if p.sel < len(shown) {
			u.pickBase(st, items[shown[p.sel]])
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &p.filter, Name: key.NameEscape},
			key.Filter{Focus: &p.filter, Name: key.NameUpArrow}, key.Filter{Focus: &p.filter, Name: key.NameDownArrow})
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameEscape:
			p.open, r.focus = false, true
			return false
		case key.NameUpArrow:
			p.sel = max(p.sel-1, 0)
		case key.NameDownArrow:
			p.sel = min(p.sel+1, max(len(shown)-1, 0))
		}
		p.list.ScrollTo(p.sel)
	}
	for {
		ev, ok := p.filter.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			pick()
			return p.open
		case widget.ChangeEvent:
			p.sel = 0
			p.list.Position = gl.Position{}
		}
	}
	return true
}

// drawBases draws the open base list under the header's base button and
// takes its clicks; a click outside it closes it.
func (u *ui) drawBases(gtx gl.Context, st *model.State) {
	th, r := u.th, &u.review
	p := &r.bases
	if !p.open {
		return
	}
	items, shown := p.shownItems()
	p.mu.Lock()
	errText := p.err
	p.mu.Unlock()
	for len(p.btns) < len(items) {
		p.btns = append(p.btns, widget.Clickable{})
	}
	for n, i := range shown {
		if p.btns[i].Clicked(gtx) {
			p.sel = n
			u.pickBase(st, items[i])
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.outside, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			p.open, r.focus = false, true
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	area := image.Rectangle{Max: gtx.Constraints.Max}
	c := clip.Rect(area).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.outside)
	c.Pop()

	pad, fieldH, rowH := gtx.Dp(6), gtx.Dp(32), gtx.Dp(30)
	w := min(gtx.Dp(340), area.Dx()-2*gtx.Dp(8))
	rows := max(len(shown), 1)
	listH := min(rows*rowH, 10*rowH)
	size := image.Pt(w, pad+fieldH+pad+listH+pad)
	at := kit.Place(p.anchor, size, area, kit.BelowStart, gtx.Dp(4), gtx.Dp(8))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: size}
	kit.Surface(gtx, rect.Inset(-1), gtx.Dp(theme.RadiusPopover)+1, kit.Floating, th.BorderStrong, th.SurfaceElevated)
	c = clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, p) // clicks on the list's padding stay in it
	c.Pop()

	fg := gtx
	fg.Constraints = gl.Exact(image.Pt(w-2*pad, fieldH))
	o := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	u.field(fg, &p.filter, th.UIFont, fieldH, "Filter branches")
	o.Pop()
	if !p.focusAsked {
		gtx.Execute(key.FocusCmd{Tag: &p.filter})
		p.focusAsked = true
	}

	y := pad + fieldH + pad
	lg := gtx
	lg.Constraints = gl.Exact(image.Pt(w-2*pad, listH))
	o = op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	defer o.Pop()
	var line string
	switch {
	case items == nil && errText == "":
		line = "Reading branches…"
	case errText != "":
		line = errText
	case len(shown) == 0:
		line = "No branch matches"
	}
	if line != "" {
		drawMid(lg, th, image.Pt(gtx.Dp(10), rowH), th.UIFont, th.Sp(theme.Body), th.Muted, line, w-2*pad)
		return
	}
	cur := r.picked()
	p.list.Axis = gl.Vertical
	row := image.Pt(w-2*pad, rowH)
	p.list.Layout(lg, len(shown), func(gtx gl.Context, n int) gl.Dimensions {
		i := shown[n]
		it := items[i]
		gtx.Constraints = gl.Exact(row)
		return p.btns[i].Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			rr := clip.UniformRRect(image.Rectangle{Max: row}, gtx.Dp(5))
			switch {
			case n == p.sel:
				paint.FillShape(gtx.Ops, th.SelectedBg, rr.Op(gtx.Ops))
			case p.btns[i].Hovered():
				paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
			}
			pointer.CursorPointer.Add(gtx.Ops)
			is := gtx.Dp(14)
			if it.ref == cur {
				drawIconAt(gtx, image.Pt(gtx.Dp(8), (rowH-is)/2), func(gtx gl.Context) { sidebar.Icon(gtx, "check", is, th.Fg) })
			}
			right := row.X - gtx.Dp(10)
			if it.note != "" {
				right -= drawRight(gtx, th, image.Pt(right, rowH), th.UIFont, th.Sp(theme.Caption), th.Muted, it.note) + gtx.Dp(10)
			}
			drawMid(gtx, th, image.Pt(gtx.Dp(30), rowH), th.MonoFont, th.Sp(theme.Body), th.Fg, it.label, right)
			return gl.Dimensions{Size: row}
		})
	})
}
