package app

import (
	"fmt"
	"image"
	"image/color"
	"path"
	"strings"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/review"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// maxLineRunes is how much of a long line the view shapes; the rest is
// past the right edge anyway.
const maxLineRunes = 400

// drawReview draws the review view into the constraints (the pane area):
// a header, the changed files on the left, the selected file's diff on the
// right.
func (u *ui) drawReview(gtx gl.Context, st *model.State) {
	th, r := u.th, &u.review
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: size}.Op())
	event.Op(gtx.Ops, &r.tag)
	files, base, root, errText, loaded := r.snapshot()
	r.follow(files)

	headH := u.reviewHeader(gtx, st, files, base, root)
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, headH), Max: image.Pt(size.X, headH+1)}.Op())
	body := image.Rect(0, headH+1, size.X, size.Y)
	switch {
	case errText != "" && len(files) == 0:
		u.reviewMessage(gtx, body, th.Red, errText)
		return
	case !loaded:
		u.reviewMessage(gtx, body, th.Muted, "Reading the diff…")
		return
	case len(files) == 0:
		u.reviewMessage(gtx, body, th.Muted, "No changes from "+gitstat.BranchName(base)+".")
		return
	}
	listW := min(gtx.Dp(300), size.X/3)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Min: body.Min, Max: image.Pt(listW, body.Max.Y)}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(listW, body.Min.Y), Max: image.Pt(listW+1, body.Max.Y)}.Op())
	o := op.Offset(body.Min).Push(gtx.Ops)
	lg := gtx
	lg.Constraints = gl.Exact(image.Pt(listW, body.Dy()))
	u.reviewFiles(lg, files, root)
	o.Pop()

	o = op.Offset(image.Pt(listW+1, body.Min.Y)).Push(gtx.Ops)
	dg := gtx
	dg.Constraints = gl.Exact(image.Pt(size.X-listW-1, body.Dy()))
	if f := r.file(files); f != nil {
		u.reviewDiff(dg, files, f, root)
	}
	o.Pop()
}

func (u *ui) reviewMessage(gtx gl.Context, r image.Rectangle, c color.NRGBA, s string) {
	g := gtx
	g.Constraints = gl.Constraints{Max: image.Pt(r.Dx()-gtx.Dp(48), r.Dy())}
	defer op.Offset(r.Min.Add(image.Pt(gtx.Dp(24), gtx.Dp(24)))).Push(gtx.Ops).Pop()
	para(g, u.th, u.th.UIFont, 14, c, s)
}

// reviewHeader draws the branch, its base and the totals on the left and
// the view's buttons on the right, with the status line under them, and
// returns its height.
func (u *ui) reviewHeader(gtx gl.Context, st *model.State, files []review.File, base, root string) int {
	th, r := u.th, &u.review
	w := gtx.Constraints.Max.X
	pad, rowH := gtx.Dp(16), gtx.Dp(28)
	ws := findWorkspace(st, r.ws)
	branch := ""
	if ws != nil {
		branch = ws.Branch
	}
	add, del := 0, 0
	for _, f := range files {
		add, del = add+f.Add, del+f.Del
	}

	// The buttons, right to left.
	x := w - pad
	place := func(wd gl.Widget) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		d := wd(g)
		call := m.Stop()
		x -= d.Size.X
		o := op.Offset(image.Pt(x, pad)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x -= gtx.Dp(8)
	}
	pane := agentPane(st, ws)
	pending := len(r.pending(root))
	var sendOff, sendText string // why the send button is off, and its label
	switch {
	case pane == nil:
		sendOff, sendText = "no agent in this tab", "Send to agent"
	case pending == 0:
		sendOff, sendText = "no comments", "Send to "+agentName(pane)
	case pending == 1:
		sendText = "Send 1 comment to " + agentName(pane)
	default:
		sendText = fmt.Sprintf("Send %d comments to %s", pending, agentName(pane))
	}
	for r.send.Clicked(gtx) {
		if sendOff == "" {
			u.sendNotes(st, root, gtx.Now)
		}
	}
	place(func(gtx gl.Context) gl.Dimensions { return u.reviewButton(gtx, &r.send, sendText, sendOff == "", true) })
	prOff := "no tab"
	if ws != nil {
		_, prOff = sidebar.ReviewBlocked(st, *ws, ghInstalled())
	}
	for r.pr.Clicked(gtx) {
		if m := u.nav.review(st, r.ws, "create_pr"); m != nil {
			u.send(m)
		}
	}
	place(func(gtx gl.Context) gl.Dimensions {
		return u.reviewButton(gtx, &r.pr, "Create pull request", prOff == "", false)
	})
	for r.pager.Clicked(gtx) {
		if m := u.nav.review(st, r.ws, "diff_pager"); m != nil {
			r.hide()
			u.send(m)
		}
	}
	place(func(gtx gl.Context) gl.Dimensions { return u.reviewButton(gtx, &r.pager, "Open in pager", true, false) })
	// The pull request's status goes here, left of the buttons.
	place(func(gtx gl.Context) gl.Dimensions { return u.reviewPRStatus(gtx, st) })

	// The title, the branch and the totals, cut short of the buttons.
	g := gtx
	g.Constraints.Max.X = max(0, x-pad)
	left := pad
	call, sz := textCall(g, th, semibold(th.UIFont), 15, th.Fg, "Review")
	o := op.Offset(image.Pt(left, pad+(rowH-sz.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	left += sz.X + gtx.Dp(12)
	type part struct {
		c color.NRGBA
		s string
	}
	parts := []part{{th.Muted, branch}}
	if b := gitstat.BranchName(base); b != "" && b != branch {
		parts = append(parts, part{th.Muted, "into " + b})
	}
	if _, _, _, _, loaded := r.snapshot(); loaded {
		n := fmt.Sprintf("%d files", len(files))
		if len(files) == 1 {
			n = "1 file"
		}
		parts = append(parts, part{th.Green, fmt.Sprintf("+%d", add)}, part{th.Red, fmt.Sprintf("−%d", del)}, part{th.Muted, n})
	}
	for _, part := range parts {
		if part.s == "" || left >= g.Constraints.Max.X {
			continue
		}
		g.Constraints.Max.X = max(0, x-pad-left)
		call, sz := textCall(g, th, th.UIFont, 13, part.c, part.s)
		o := op.Offset(image.Pt(left, pad+(rowH-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		left += sz.X + gtx.Dp(10)
	}

	h := pad + rowH + gtx.Dp(8)
	status, col := r.status, th.Muted
	if _, _, _, errText, _ := r.snapshot(); errText != "" {
		status, col = errText, th.Red
	}
	if status == "" {
		status = "j/k file · n/p hunk · ↑/↓ line, Shift for a range · c or a line number comments · Esc closes"
	}
	g = gtx
	g.Constraints.Max.X = w - 2*pad
	call, sz = textCall(g, th, th.UIFont, 12, col, status)
	o = op.Offset(image.Pt(pad, h)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return h + sz.Y + gtx.Dp(10)
}

// reviewPRStatus is the header's slot for the branch's pull request
// status; it draws nothing yet.
func (u *ui) reviewPRStatus(gtx gl.Context, st *model.State) gl.Dimensions {
	return gl.Dimensions{}
}

// reviewButton is aide's size="sm" button, as the settings page draws
// it; an off one is muted and takes no clicks.
func (u *ui) reviewButton(gtx gl.Context, c *widget.Clickable, label string, on, primary bool) gl.Dimensions {
	th := u.th
	draw := func(gtx gl.Context) gl.Dimensions {
		h := gtx.Dp(28)
		fill, fg, border := th.SurfaceElevated, th.Fg, theme.Mix(th.SurfaceElevated, th.Fg, 0.08)
		switch {
		case !on:
			fill, fg, border = th.SurfaceSecondary, th.Muted, th.SurfaceSecondary
		case primary:
			fill, fg, border = th.Primary, th.OnPrimary, th.Primary
		}
		if on && c.Hovered() {
			fill = theme.Mix(fill, th.Fg, 0.08)
		}
		call, tsz := textCall(gtx, th, medium(th.UIFont), 13, fg, label)
		sz := image.Pt(tsz.X+2*gtx.Dp(10), h)
		rrect := func(c color.NRGBA, r image.Rectangle, rad int) {
			paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, rad).Op(gtx.Ops))
		}
		rrect(border, image.Rectangle{Max: sz}, gtx.Dp(6))
		rrect(fill, image.Rect(1, 1, sz.X-1, sz.Y-1), gtx.Dp(6)-1)
		o := op.Offset(sz.Sub(tsz).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		if on {
			defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
		}
		return gl.Dimensions{Size: sz}
	}
	if !on {
		return draw(gtx)
	}
	return c.Layout(gtx, draw)
}

// statusColor is a file status's letter color.
func (u *ui) statusColor(s byte) color.NRGBA {
	switch s {
	case 'A', '?':
		return u.th.Green
	case 'D':
		return u.th.Red
	case 'R':
		return u.th.Blue
	}
	return u.th.Yellow
}

// reviewFiles is the file list: a reviewed checkbox, the status, the path
// and the counts of each file.
func (u *ui) reviewFiles(gtx gl.Context, files []review.File, root string) {
	th, r := u.th, &u.review
	if r.fileClick == nil {
		r.fileClick, r.checkClick = map[string]*widget.Clickable{}, map[string]*widget.Clickable{}
	}
	rowH := gtx.Dp(30)
	w := gtx.Constraints.Max.X
	gl.UniformInset(8).Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		w = gtx.Constraints.Max.X
		return r.fileList.Layout(gtx, len(files), func(gtx gl.Context, i int) gl.Dimensions {
			f := &files[i]
			c, chk := r.fileClick[f.Path], r.checkClick[f.Path]
			if c == nil {
				c, chk = new(widget.Clickable), new(widget.Clickable)
				r.fileClick[f.Path], r.checkClick[f.Path] = c, chk
			}
			done := u.reviewed(root, f)
			for chk.Clicked(gtx) {
				u.markReviewed(root, files, f, !done)
				done = !done
			}
			for c.Clicked(gtx) {
				r.selectFile(files, i)
				r.focus = true
			}
			return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				sz := image.Pt(w, rowH)
				switch {
				case i == r.sel:
					paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceElevated, th.Fg, 0.04), clip.UniformRRect(image.Rectangle{Max: sz}, gtx.Dp(6)).Op(gtx.Ops))
				case c.Hovered():
					paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: sz}, gtx.Dp(6)).Op(gtx.Ops))
				}
				x := gtx.Dp(8)
				box := gtx.Dp(14)
				o := op.Offset(image.Pt(x, (rowH-box)/2)).Push(gtx.Ops)
				chk.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					drawCheck(gtx, th, box, done)
					defer clip.Rect{Max: image.Pt(box, box)}.Push(gtx.Ops).Pop()
					pointer.CursorPointer.Add(gtx.Ops)
					return gl.Dimensions{Size: image.Pt(box, box)}
				})
				o.Pop()
				x += box + gtx.Dp(10)
				x += drawMid(gtx, th, image.Pt(x, rowH), medium(th.MonoFont), 12, u.statusColor(f.Status), string(rune(f.Status)), w) + gtx.Dp(8)
				counts := ""
				if f.Binary {
					counts = "bin"
				}
				right := w - gtx.Dp(8)
				if !f.Binary {
					right -= drawRight(gtx, th, image.Pt(right, rowH), th.UIFont, 12, th.Red, fmt.Sprintf("−%d", f.Del)) + gtx.Dp(6)
					right -= drawRight(gtx, th, image.Pt(right, rowH), th.UIFont, 12, th.Green, fmt.Sprintf("+%d", f.Add))
				} else {
					right -= drawRight(gtx, th, image.Pt(right, rowH), th.UIFont, 12, th.Muted, counts)
				}
				dir, name := path.Split(f.Path)
				fg := th.Fg
				if done {
					fg = th.Muted
				}
				g := gtx
				g.Constraints.Max.X = max(0, right-gtx.Dp(8))
				x += drawMid(g, th, image.Pt(x, rowH), th.UIFont, 13, fg, name, g.Constraints.Max.X)
				if dir != "" {
					drawMid(g, th, image.Pt(x+gtx.Dp(6), rowH), th.UIFont, 12, th.Muted, strings.TrimSuffix(dir, "/"), g.Constraints.Max.X)
				}
				defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: sz}
			})
		})
	})
}

// drawMid draws s left-aligned at at.X, centered in a row at.Y tall, cut
// at maxX, and returns its width.
func drawMid(gtx gl.Context, th *theme.Theme, at image.Point, f font.Font, size unit.Sp, c color.NRGBA, s string, maxX int) int {
	g := gtx
	g.Constraints.Max.X = max(0, maxX-at.X)
	call, sz := textCall(g, th, f, size, c, s)
	defer op.Offset(image.Pt(at.X, (at.Y-sz.Y)/2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return sz.X
}

// drawRight draws s right-aligned to at.X, centered in a row at.Y tall,
// and returns its width.
func drawRight(gtx gl.Context, th *theme.Theme, at image.Point, f font.Font, size unit.Sp, c color.NRGBA, s string) int {
	call, sz := textCall(gtx, th, f, size, c, s)
	defer op.Offset(image.Pt(at.X-sz.X, (at.Y-sz.Y)/2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return sz.X
}

// drawCheck draws the reviewed checkbox, size px square.
func drawCheck(gtx gl.Context, th *theme.Theme, size int, on bool) {
	rect := image.Rect(0, 0, size, size)
	rad := gtx.Dp(3)
	if !on {
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.25), clip.UniformRRect(rect, rad).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Bg, clip.UniformRRect(rect.Inset(1), rad-1).Op(gtx.Ops))
		return
	}
	paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(rect, rad).Op(gtx.Ops))
	var p clip.Path
	p.Begin(gtx.Ops)
	s := float32(size) / 16
	p.MoveTo(f32.Pt(4*s, 8.5*s))
	p.LineTo(f32.Pt(7*s, 11.5*s))
	p.LineTo(f32.Pt(12*s, 5*s))
	paint.FillShape(gtx.Ops, th.OnPrimary, clip.Stroke{Path: p.End(), Width: 2 * s}.Op())
}

// statusWord names a file status for the diff's header.
func statusWord(f *review.File) string {
	switch f.Status {
	case 'A':
		return "Added"
	case 'D':
		return "Deleted"
	case 'R':
		return "Renamed from " + f.OldPath
	case '?':
		return "Untracked"
	}
	return "Modified"
}

// reviewDiff draws the selected file: its header with Mark reviewed and
// Discard changes, then its rows, laid out only as far as they show.
func (u *ui) reviewDiff(gtx gl.Context, files []review.File, f *review.File, root string) {
	th, r := u.th, &u.review
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(16)
	headH := gtx.Dp(48)

	done := u.reviewed(root, f)
	for r.mark.Clicked(gtx) {
		u.markReviewed(root, files, f, !done)
		done = !done
	}
	for r.discard.Clicked(gtx) {
		r.discarding = *f
		u.modal.open(modalDiscard, r.ws)
	}
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.Rect{Max: image.Pt(w, headH)}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, headH), Max: image.Pt(w, headH+1)}.Op())
	x := w - pad
	for _, b := range []struct {
		c     *widget.Clickable
		label string
	}{{&r.discard, "Discard changes"}, {&r.mark, map[bool]string{false: "Mark reviewed", true: "Reviewed"}[done]}} {
		m := op.Record(gtx.Ops)
		d := u.reviewButton(gtx, b.c, b.label, true, false)
		call := m.Stop()
		x -= d.Size.X
		o := op.Offset(image.Pt(x, (headH-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x -= gtx.Dp(8)
	}
	g := gtx
	g.Constraints.Max.X = max(0, x-pad)
	nx := pad + drawMid(g, th, image.Pt(pad, headH), medium(th.MonoFont), 13, th.Fg, f.Path, g.Constraints.Max.X)
	drawMid(g, th, image.Pt(nx+gtx.Dp(10), headH), th.UIFont, 12, th.Muted, statusWord(f), g.Constraints.Max.X)

	top := headH + 1
	notes := notesAt(r.notes[root], f)
	if lost := notes[-1]; len(lost) > 0 {
		for _, n := range lost {
			o := op.Offset(image.Pt(pad, top+gtx.Dp(8))).Push(gtx.Ops)
			ng := gtx
			ng.Constraints = gl.Constraints{Max: image.Pt(w-2*pad, gtx.Constraints.Max.Y)}
			d := u.reviewNote(ng, root, n, true)
			o.Pop()
			top += d.Size.Y + gtx.Dp(8)
		}
		top += gtx.Dp(8)
	}
	body := image.Rect(0, top, w, gtx.Constraints.Max.Y)
	if f.Binary || r.huge(f) {
		msg := "Binary file, not shown."
		switch {
		case f.Status == '?' && f.Binary:
			msg = "Binary, or larger than 1 MiB: not shown."
		case !f.Binary:
			msg = fmt.Sprintf("%d changed lines.", f.Add+f.Del)
		}
		u.reviewMessage(gtx, body, th.Muted, msg)
		if !f.Binary {
			for r.showHuge.Clicked(gtx) {
				if r.shownHuge == nil {
					r.shownHuge = map[string]bool{}
				}
				r.shownHuge[f.Path], r.rowsDirty = true, true
			}
			o := op.Offset(body.Min.Add(image.Pt(gtx.Dp(24), gtx.Dp(56)))).Push(gtx.Ops)
			u.reviewButton(gtx, &r.showHuge, "Show the diff", true, false)
			o.Pop()
		}
		return
	}
	rows := r.rowsOf(f)
	if r.scrollTo >= 0 {
		p := &r.diffList.Position
		if r.scrollTo < p.First || r.scrollTo >= p.First+max(p.Count-1, 1) {
			p.First, p.Offset = max(0, r.scrollTo-4), 0
		}
		r.scrollTo = -1
	}
	u.reviewEditor(gtx, f, root)
	o := op.Offset(body.Min).Push(gtx.Ops)
	lg := gtx
	lg.Constraints = gl.Exact(body.Size())
	m := u.lineMetrics(lg, f)
	r.diffList.Layout(lg, len(rows), func(gtx gl.Context, i int) gl.Dimensions {
		return u.reviewRow(gtx, f, rows[i], m, notes, root)
	})
	o.Pop()
}

// lineMetrics is the diff's row height and its number columns' width.
type lineMetrics struct {
	h, num, mark int
}

func (u *ui) lineMetrics(gtx gl.Context, f *review.File) lineMetrics {
	last := 0
	if n := len(f.Lines); n > 0 {
		last = max(f.Lines[n-1].Old, f.Lines[n-1].New)
	}
	digits := max(len(fmt.Sprint(last)), 3)
	_, sz := textCall(gtx, u.th, u.th.MonoFont, u.monoSize(), u.th.Fg, strings.Repeat("0", digits))
	return lineMetrics{h: sz.Y + gtx.Dp(4), num: sz.X + gtx.Dp(16), mark: sz.X/digits + gtx.Dp(8)}
}

func (u *ui) monoSize() unit.Sp { return u.th.MonoSize * 0.9 }

// reviewRow draws one row of the diff, and under a line the comments on
// it and the comment field when it is open there.
func (u *ui) reviewRow(gtx gl.Context, f *review.File, row review.Row, m lineMetrics, notes map[int][]*note, root string) gl.Dimensions {
	th, r := u.th, &u.review
	w := gtx.Constraints.Max.X
	gutter := 2 * m.num
	switch row.Kind {
	case 'h':
		sz := image.Pt(w, m.h+gtx.Dp(6))
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Blue, 0.08), clip.Rect{Max: sz}.Op())
		drawMid(gtx, th, image.Pt(gutter+m.mark, sz.Y), th.MonoFont, u.monoSize(), th.Muted, row.Text, w)
		return gl.Dimensions{Size: sz}
	case 'f':
		c := r.foldClick[row.Line]
		if c == nil {
			if r.foldClick == nil {
				r.foldClick = map[int]*widget.Clickable{}
			}
			c = new(widget.Clickable)
			r.foldClick[row.Line] = c
		}
		for c.Clicked(gtx) {
			r.openFold(f.Path, row.Line)
		}
		return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			sz := image.Pt(w, m.h+gtx.Dp(6))
			bg := th.SurfaceSecondary
			if c.Hovered() {
				bg = th.SurfaceElevated
			}
			paint.FillShape(gtx.Ops, bg, clip.Rect{Max: sz}.Op())
			s := fmt.Sprintf("⋯  %d unchanged lines", row.N)
			if row.N == 1 {
				s = "⋯  1 unchanged line"
			}
			drawMid(gtx, th, image.Pt(gutter+m.mark, sz.Y), th.UIFont, 12, th.Muted, s, w)
			defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
			return gl.Dimensions{Size: sz}
		})
	}
	i := row.Line
	l := f.Lines[i]
	gc, lc := r.gutterClick[i], r.lineClick[i]
	if gc == nil {
		if r.gutterClick == nil {
			r.gutterClick, r.lineClick = map[int]*widget.Clickable{}, map[int]*widget.Clickable{}
		}
		gc, lc = new(widget.Clickable), new(widget.Clickable)
		r.gutterClick[i], r.lineClick[i] = gc, lc
	}
	for {
		c, ok := gc.Update(gtx)
		if !ok {
			break
		}
		r.setLine(i, c.Modifiers.Contain(key.ModShift))
		r.startComment()
		r.focus = true
	}
	for {
		c, ok := lc.Update(gtx)
		if !ok {
			break
		}
		r.setLine(i, c.Modifiers.Contain(key.ModShift))
		r.editing, r.focus = false, true
	}

	bg, numBg, markC := th.Surface, th.Surface, th.Muted
	switch l.Kind {
	case '+':
		bg, numBg, markC = theme.Mix(th.Surface, th.Green, 0.10), theme.Mix(th.Surface, th.Green, 0.16), th.Green
	case '-':
		bg, numBg, markC = theme.Mix(th.Surface, th.Red, 0.10), theme.Mix(th.Surface, th.Red, 0.16), th.Red
	}
	if lo, hi := r.span(); r.cur >= 0 && i >= lo && i <= hi {
		bg, numBg = theme.Mix(bg, th.Primary, 0.16), theme.Mix(numBg, th.Primary, 0.3)
	}
	h := m.h
	paint.FillShape(gtx.Ops, bg, clip.Rect{Max: image.Pt(w, h)}.Op())
	paint.FillShape(gtx.Ops, numBg, clip.Rect{Max: image.Pt(gutter, h)}.Op())
	num := func(n, right int) {
		if n > 0 {
			drawRight(gtx, th, image.Pt(right, h), th.MonoFont, u.monoSize(), th.Muted, fmt.Sprint(n))
		}
	}
	num(l.Old, m.num-gtx.Dp(8))
	num(l.New, 2*m.num-gtx.Dp(8))
	if l.Kind != ' ' {
		drawMid(gtx, th, image.Pt(gutter+gtx.Dp(6), h), th.MonoFont, u.monoSize(), markC, string(rune(l.Kind)), w)
	}
	text := strings.ReplaceAll(l.Text, "\t", "    ")
	if utf8.RuneCountInString(text) > maxLineRunes {
		text = string([]rune(text)[:maxLineRunes])
	}
	drawMid(gtx, th, image.Pt(gutter+m.mark, h), th.MonoFont, u.monoSize(), th.Fg, text, w)
	o := op.Offset(image.Point{}).Push(gtx.Ops)
	gg := gtx
	gg.Constraints = gl.Exact(image.Pt(gutter, h))
	gc.Layout(gg, func(gtx gl.Context) gl.Dimensions {
		defer clip.Rect{Max: image.Pt(gutter, h)}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: image.Pt(gutter, h)}
	})
	o.Pop()
	o = op.Offset(image.Pt(gutter, 0)).Push(gtx.Ops)
	lg := gtx
	lg.Constraints = gl.Exact(image.Pt(w-gutter, h))
	lc.Layout(lg, func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(w-gutter, h)} })
	o.Pop()

	// Comments and the comment field hang under the line, past the gutter.
	y := h
	ng := gtx
	ng.Constraints = gl.Constraints{Max: image.Pt(min(w-gutter-gtx.Dp(24), gtx.Dp(720)), gtx.Constraints.Max.Y)}
	for _, n := range notes[i] {
		o := op.Offset(image.Pt(gutter+gtx.Dp(12), y+gtx.Dp(6))).Push(gtx.Ops)
		d := u.reviewNote(ng, root, n, false)
		o.Pop()
		y += d.Size.Y + gtx.Dp(6)
	}
	if r.editing && i == r.editTo {
		o := op.Offset(image.Pt(gutter+gtx.Dp(12), y+gtx.Dp(6))).Push(gtx.Ops)
		d := u.reviewField(ng, f)
		o.Pop()
		y += d.Size.Y + gtx.Dp(6)
	}
	if y > h {
		y += gtx.Dp(6)
	}
	return gl.Dimensions{Size: image.Pt(w, y)}
}

// reviewEditor handles the comment field's submit.
func (u *ui) reviewEditor(gtx gl.Context, f *review.File, root string) {
	r := &u.review
	for {
		ev, ok := r.edit.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok && r.editing {
			r.addComment(f, root)
		}
	}
}

// reviewField is the comment field, with the lines it comments on.
func (u *ui) reviewField(gtx gl.Context, f *review.File) gl.Dimensions {
	th, r := u.th, &u.review
	ref := review.Comment{Path: f.Path, Lines: f.Lines[r.editFrom : r.editTo+1]}.Ref()
	return boxedCard(gtx, th, th.SurfaceElevated, theme.Mix(th.SurfaceElevated, th.Primary, 0.6), func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.UIFont, 12, th.Muted, "Comment on "+ref+" · Enter saves · Esc cancels")
			}),
			gl.Rigid(gl.Spacer{Height: 6}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return r.edit.Layout(gtx, th.Shaper, th.UIFont, 13, colorCall(gtx, th.Fg), colorCall(gtx, theme.Mix(th.SurfaceElevated, th.Primary, 0.35)))
			}),
		)
	})
}

// reviewNote draws one comment: where it is, whether it went to the
// agent, its text, and Delete while it is pending.
func (u *ui) reviewNote(gtx gl.Context, root string, n *note, lost bool) gl.Dimensions {
	th, r := u.th, &u.review
	for n.del.Clicked(gtx) {
		if n.state == notePending {
			ns := r.notes[root]
			for i := range ns {
				if ns[i] == n {
					r.notes[root] = append(ns[:i:i], ns[i+1:]...)
					break
				}
			}
		}
	}
	state, col := "Pending", th.Yellow
	switch n.state {
	case noteQueued:
		state, col = "Queued", th.Blue
	case noteSent:
		state, col = "Sent", th.Green
	}
	label := state
	if lost {
		label += " · " + n.Ref() + ", no longer in the diff"
	} else if len(n.Lines) > 1 {
		label += " · " + n.Ref()
	}
	fill := th.SurfaceElevated
	if n.state == noteSent {
		fill = th.SurfaceSecondary
	}
	return boxedCard(gtx, th, fill, theme.Mix(fill, th.Fg, 0.1), func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				w := gtx.Constraints.Max.X
				call, sz := textCall(gtx, th, medium(th.UIFont), 12, col, label)
				call.Add(gtx.Ops)
				if n.state == notePending {
					c := th.Muted
					if n.del.Hovered() {
						c = th.Red
					}
					dc, dsz := textCall(gtx, th, th.UIFont, 12, c, "Delete")
					o := op.Offset(image.Pt(w-dsz.X, 0)).Push(gtx.Ops)
					n.del.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
						dc.Add(gtx.Ops)
						defer clip.Rect{Max: dsz}.Push(gtx.Ops).Pop()
						pointer.CursorPointer.Add(gtx.Ops)
						return gl.Dimensions{Size: dsz}
					})
					o.Pop()
				}
				return gl.Dimensions{Size: image.Pt(w, sz.Y)}
			}),
			gl.Rigid(gl.Spacer{Height: 4}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				c := th.Fg
				if n.state == noteSent {
					c = th.Muted
				}
				return para(gtx, th, th.UIFont, 13, c, n.Text)
			}),
		)
	})
}

// boxedCard draws w on a rounded fill with a 1px border, as wide as the
// constraints allow.
func boxedCard(gtx gl.Context, th *theme.Theme, fill, border color.NRGBA, w gl.Widget) gl.Dimensions {
	pad := image.Pt(gtx.Dp(12), gtx.Dp(8))
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = gl.Constraints{Min: image.Pt(gtx.Constraints.Max.X-2*pad.X, 0), Max: gtx.Constraints.Max.Sub(pad.Mul(2))}
	d := w(g)
	call := m.Stop()
	sz := image.Pt(gtx.Constraints.Max.X, d.Size.Y+2*pad.Y)
	r := gtx.Dp(6)
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(image.Rectangle{Max: sz}, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(1, 1, sz.X-1, sz.Y-1), r-1).Op(gtx.Ops))
	o := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: sz}
}

// discardBody is the discard dialog for the review view's file.
func (u *ui) discardBody(gtx gl.Context) gl.Dimensions {
	th, f := u.th, &u.review.discarding
	base := gitstat.BranchName(u.review.base)
	what := "Puts it back as it is at the merge base with " + base + ", on disk and staged. " +
		"Commits on the branch stay, so the undo shows as an uncommitted change. Uncommitted edits to it are lost."
	switch f.Status {
	case '?':
		what = "Deletes the untracked file. It cannot be brought back."
	case 'R':
		what = "Puts " + f.OldPath + " back and removes " + f.Path + ", on disk and staged, as at the merge base with " + base +
			". Commits on the branch stay, so the undo shows as an uncommitted change. Uncommitted edits to it are lost."
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Discard changes to "+path.Base(f.Path)+"?")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.MonoFont, 12, th.Muted, f.Path)
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 14, th.Muted, what)
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Discard", th.Red, theme.Hex("#ffffff"))
		}),
	)
}
