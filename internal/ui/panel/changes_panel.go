package panel

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Lucide's file-diff, external-link and refresh-cw, for the side panel
// and the chat header's button that opens it.
const (
	IconFileDiff = "M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7ZM9 10h6M12 13V7M9 17h6"
	icExternal   = "M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"
	icRefresh    = "M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8M21 3v5h-5M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16M8 16H3v5"
)

// The side panel's width: 45% of the chat's, between sideMin and
// sideMax; a chat that would keep less than chatMin gives it all.
const (
	sideMin = 360
	sideMax = 720
	chatMin = 420
	sidePad = theme.SpaceL
)

// side is the Files and Changes panel at the right of the chat: a turn's
// diff per file, or a file on disk, read-only.
type side struct {
	on   bool
	file bool   // the File view; else Changes
	turn string // Changes: the turn's first item id, "" for the newest that edited
	jump string // Changes: a file to scroll to, once
	path string // File: the file as asked, relative to Dir or absolute

	pv    *preview
	img   paint.ImageOp
	lines []string
	long  int // runes in pv's longest line
	md    mdview.View

	list, rows layout.List
	hs         gesture.Scroll
	hoff       int

	tipOf              *widget.Clickable
	tipSince, tipShown time.Time
	peekOf             string // the change card's row the pointer rests on
	peekSince          time.Time
}

// changeCache is a turn's changes, worked out once per change of its
// items.
type changeCache struct {
	sig string
	fs  []fileChange
}

// FilesOpen reports whether the Files and Changes panel shows.
func (t *Transcript) FilesOpen() bool { return t.side.on }

// ToggleFiles shows the panel at the newest turn's changes, or hides it.
func (t *Transcript) ToggleFiles() {
	if t.side.on {
		t.CloseFiles()
		return
	}
	t.showChanges("", "")
}

// CloseFiles hides the panel, and reports whether it showed: Esc closes
// it before it stops a turn.
func (t *Transcript) CloseFiles() bool {
	was := t.side.on
	t.side.on = false
	t.side.md.Close()
	return was
}

// showChanges shows turn's changes, scrolled to file when it is one.
func (t *Transcript) showChanges(turn, file string) {
	s := &t.side
	s.on, s.file, s.turn, s.jump = true, false, turn, file
	s.list.Position = layout.Position{}
}

// showFile shows file p in the File view, read again.
func (t *Transcript) showFile(p string) {
	s := &t.side
	s.on, s.file, s.path = true, true, p
	t.readSide()
}

// readSide reads the File view's file again.
func (t *Transcript) readSide() {
	s := &t.side
	pv := readPreview(t.Dir, s.path)
	s.pv, s.lines, s.long, s.hoff = &pv, nil, 0, 0
	s.rows.Position = layout.Position{}
	s.md.Close()
	switch pv.kind {
	case previewPicture:
		s.img = paint.NewImageOp(pv.img)
	case previewText:
		s.lines = strings.Split(strings.TrimSuffix(pv.text, "\n"), "\n")
		for _, l := range s.lines {
			s.long = max(s.long, utf8.RuneCountInString(mdview.Detab(l)))
		}
	case previewMD:
		root := t.Dir
		s.md.Fetch = func(_ context.Context, p string) ([]byte, error) { return readInside(root, p) }
		s.md.OpenURL = t.openTarget
		s.md.Invalidate = t.Invalidate
		_ = s.md.Open(pv.path, "")
	}
}

// readInside reads p for the markdown view: only under root, through its
// links, and at most maxPicture bytes.
func readInside(root, p string) ([]byte, error) {
	real, err := insideRoot(root, p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPicture+1))
	if err == nil && len(b) > maxPicture {
		return nil, errors.New("over 5 MiB")
	}
	return b, err
}

// localFile is target as a file under Dir for the File view: a path,
// relative to Dir or absolute, or a file URL; "" for a URL, a folder,
// what is not there, and a file outside Dir, which Open still opens.
func (t *Transcript) localFile(target string) string {
	p := target
	if strings.HasPrefix(target, "file://") {
		u, err := url.Parse(target)
		if err != nil {
			return ""
		}
		p = u.Path
	} else if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return ""
	}
	abs := p
	if !filepath.IsAbs(abs) {
		if t.Dir == "" {
			return ""
		}
		abs = filepath.Join(t.Dir, abs)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	if _, err := insideRoot(t.Dir, abs); err != nil {
		return ""
	}
	return abs
}

// sideWidth is the panel's width in a chat full wide, 0 while it is shut.
func (t *Transcript) sideWidth(gtx layout.Context, full int) int {
	if !t.side.on {
		return 0
	}
	w := min(max(full*45/100, gtx.Dp(sideMin)), gtx.Dp(sideMax))
	if full-w < gtx.Dp(chatMin) {
		return full
	}
	return w
}

// changesOf is block b's changes, cached.
func (t *Transcript) changesOf(d *cdraw, b block) []fileChange {
	c := d.s.Content
	if b.kind != blockTurn || b.lo >= len(c) || b.hi > len(c) {
		return nil
	}
	sig := strconv.Itoa(b.hi) + c[b.hi-1].ID + c[b.hi-1].UpdatedAt
	id := c[b.lo].ID
	if t.changes == nil {
		t.changes = map[string]changeCache{}
	}
	if cc, ok := t.changes[id]; ok && cc.sig == sig {
		return cc.fs
	}
	fs := turnChanges(c, b.lo, b.hi, t.Dir, d.diff)
	t.changes[id] = changeCache{sig: sig, fs: fs}
	return fs
}

// sideTurn is the block the Changes view shows: the turn asked for, else
// the newest that changed a file; ok is false for none.
func (t *Transcript) sideTurn(d *cdraw) (block, bool) {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		b := t.blocks[i]
		if b.kind != blockTurn || b.lo >= len(d.s.Content) {
			continue
		}
		if t.side.turn != "" {
			if d.s.Content[b.lo].ID == t.side.turn {
				return b, true
			}
			continue
		}
		if len(t.changesOf(d, b)) > 0 {
			return b, true
		}
	}
	return block{}, false
}

// promptBefore is the prompt that started the turn at block i, "" for
// none.
func (t *Transcript) promptBefore(s *session.Snapshot, b block) string {
	for i := b.lo - 1; i >= 0; i-- {
		if s.Content[i].Kind == session.KindUserPrompt {
			return firstLine(s.Content[i].Text)
		}
	}
	return ""
}

// sidePanel draws the panel in size: its header, then the Changes or the
// File view.
func (t *Transcript) sidePanel(gtx layout.Context, d *cdraw, size image.Point) {
	th := d.th
	s := &t.side
	gtx.Constraints = layout.Exact(size)
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Sidebar, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(gtx.Dp(1), size.Y)}.Op())

	b, haveTurn := t.sideTurn(d)
	var fs []fileChange
	if haveTurn {
		fs = t.changesOf(d, b)
	}
	if t.click("side:changes").Clicked(gtx) {
		s.file = false
	}
	if t.click("side:file").Clicked(gtx) {
		s.file = true
		if s.path == "" && len(fs) > 0 {
			t.showFile(fs[0].path)
		}
	}
	for t.click("side:close").Clicked(gtx) {
		t.CloseFiles()
	}
	for t.click("side:refresh").Clicked(gtx) {
		t.readSide()
	}
	for t.click("side:open").Clicked(gtx) && s.pv != nil {
		t.openOutside(s.pv.path)
	}
	if !s.on {
		return
	}

	head := gtx.Dp(rowH + theme.SpaceM + theme.Space2XS) // 44
	t.sideHead(gtx, d, size.X, head, len(fs))
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, head-1), Max: image.Pt(size.X, head)}.Op())
	body := image.Pt(size.X, max(0, size.Y-head))
	o := op.Offset(image.Pt(0, head)).Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(body)
	cl := clip.Rect{Max: body}.Push(gtx.Ops)
	if s.file {
		t.fileView(g, d, body)
	} else {
		t.changesView(g, d, b, haveTurn, fs, body)
	}
	cl.Pop()
	o.Pop()
}

// sideHead is the panel's 44 dp header: Changes and File, and on the
// right Refresh and Open outside for a file, and Close.
func (t *Transcript) sideHead(gtx layout.Context, d *cdraw, w, h, files int) {
	s := &t.side
	pad := gtx.Dp(theme.SpaceS)
	count := ""
	if files > 0 {
		count = strconv.Itoa(files)
	}
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, gtx.Dp(theme.Space2XS),
			func(gtx layout.Context) layout.Dimensions {
				return d.tabButton(gtx, t.click("side:changes"), "Changes", count, !s.file)
			},
			func(gtx layout.Context) layout.Dimensions {
				return d.tabButton(gtx, t.click("side:file"), "File", "", s.file)
			})
	})
	at(gtx, image.Pt(pad, (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	type btn struct {
		id, ic, tip string
	}
	bs := []btn{{"side:close", "", "Close (Esc)"}}
	if s.file && s.pv != nil {
		bs = append([]btn{{"side:refresh", icRefresh, "Read again"}, {"side:open", icExternal, "Open outside pitwall"}}, bs...)
	}
	x := w - pad
	for i := len(bs) - 1; i >= 0; i-- {
		x -= gtx.Dp(hit)
		b := bs[i]
		at(gtx, image.Pt(x, (h-gtx.Dp(hit))/2), func(gtx layout.Context) layout.Dimensions {
			return d.iconButton(gtx, t.click(b.id), b.ic, b.tip)
		})
		x -= gtx.Dp(theme.Space2XS)
	}
}

// tabButton is one of the header's two views: a label, a count, a fill
// when it shows.
func (d *cdraw) tabButton(gtx layout.Context, c *widget.Clickable, label, count string, on bool) layout.Dimensions {
	th := d.th
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		h, px := gtx.Dp(hit), gtx.Dp(theme.SpaceS+theme.Space2XS)
		fg := th.Muted
		if on {
			fg = th.Fg
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return hrow(gtx, gtx.Dp(6),
				func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), fg, label)
				},
				func(gtx layout.Context) layout.Dimensions {
					if count == "" {
						return layout.Dimensions{}
					}
					return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, count)
				})
		})
		w := ld.Size.X + 2*px
		r := image.Rect(0, 0, w, h)
		rad := gtx.Dp(theme.RadiusControl)
		switch {
		case c.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(r, rad).Op(gtx.Ops))
		case on:
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(r, rad).Op(gtx.Ops))
		case c.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(r, rad).Op(gtx.Ops))
		}
		at(gtx, image.Pt(px, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// iconButton is a 28 dp square ghost button with icon path ic, x for "",
// and tip under it after the pointer rests.
func (d *cdraw) iconButton(gtx layout.Context, c *widget.Clickable, ic, tip string) layout.Dimensions {
	th := d.th
	dims := clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(hit)
		r := image.Rect(0, 0, h, h)
		rad := gtx.Dp(theme.RadiusControl)
		col := th.Muted
		switch {
		case c.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(r, rad).Op(gtx.Ops))
			col = th.Fg
		case c.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(r, rad).Op(gtx.Ops))
			col = th.Fg
		}
		is := gtx.Dp(14)
		at(gtx, image.Pt((h-is)/2, (h-is)/2), func(gtx layout.Context) layout.Dimensions {
			if ic == "" {
				return sidebar.Icon(gtx, "x", is, col)
			}
			return sidebar.IconPath(gtx, ic, is, col)
		})
		return layout.Dimensions{Size: r.Max}
	})
	d.tip(gtx, c, dims.Size, tip)
	return dims
}

// tipDelay is how long the pointer rests on a button before its tip
// shows; tipChain, how soon after one tip the next shows at once.
const (
	tipDelay = 500 * time.Millisecond
	tipChain = 300 * time.Millisecond
)

// tip draws tip under a button of size, once the pointer rested on c.
func (d *cdraw) tip(gtx layout.Context, c *widget.Clickable, size image.Point, tip string) {
	s := &d.t.side
	if !c.Hovered() {
		if s.tipOf == c {
			s.tipOf = nil
		}
		return
	}
	if s.tipOf != c {
		s.tipOf, s.tipSince = c, gtx.Now
	}
	if gtx.Now.Sub(s.tipSince) < tipDelay && gtx.Now.Sub(s.tipShown) > tipChain {
		gtx.Execute(op.InvalidateCmd{At: s.tipSince.Add(tipDelay)})
		return
	}
	s.tipShown = gtx.Now
	th := d.th
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Fg, tip)
	})
	pad := image.Pt(gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceXS))
	box := td.Size.Add(pad.Mul(2))
	p := image.Pt(size.X-box.X, size.Y+gtx.Dp(theme.SpaceXS))
	m := op.Record(gtx.Ops)
	o := op.Offset(p).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: box}, gtx.Dp(theme.RadiusControl), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, pad, func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

// changesView is the turn's diff per file: a line with what the turn
// asked and its totals, then each file's path and its hunks.
func (t *Transcript) changesView(gtx layout.Context, d *cdraw, b block, ok bool, fs []fileChange, size image.Point) {
	s := &t.side
	if !ok || len(fs) == 0 {
		msg := "No file edits in this chat yet"
		if ok {
			msg = "This turn did not edit a file"
		}
		d.sideNote(gtx, "pencil", msg)
		return
	}
	if s.jump != "" {
		for i, f := range fs {
			if f.path == s.jump {
				s.list.Position = layout.Position{First: i + 1}
			}
		}
		s.jump = ""
	}
	s.list.Axis = layout.Vertical
	pad := gtx.Dp(sidePad)
	w := size.X - 2*pad
	turnID := d.s.Content[b.lo].ID
	s.list.Layout(gtx, len(fs)+2, func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(size.X, 0), Max: image.Pt(size.X, 1<<20)}
		switch {
		case i == 0:
			return layout.Inset{Top: sidePad, Bottom: theme.SpaceM, Left: sidePad, Right: sidePad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = w
				return d.changesTitle(gtx, fs, t.promptBefore(d.s, b))
			})
		case i == len(fs)+1:
			return layout.Dimensions{Size: image.Pt(size.X, pad)}
		}
		f := fs[i-1]
		return layout.Inset{Left: sidePad, Right: sidePad, Bottom: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
			return vstack(gtx, gtx.Dp(theme.SpaceXS),
				func(gtx layout.Context) layout.Dimensions { return d.changeFileHead(gtx, turnID, f) },
				func(gtx layout.Context) layout.Dimensions { return d.changeDiff(gtx, turnID, f, 0) })
		})
	})
}

// changesTitle is "Edited 3 files +12 −4", and under it the turn's prompt.
func (d *cdraw) changesTitle(gtx layout.Context, fs []fileChange, prompt string) layout.Dimensions {
	th := d.th
	add, del := changeTotals(fs)
	return vstack(gtx, gtx.Dp(theme.SpaceXS),
		func(gtx layout.Context) layout.Dimensions {
			return hrow(gtx, gtx.Dp(theme.SpaceS),
				func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, editedTitle(fs))
				},
				func(gtx layout.Context) layout.Dimensions { return d.counts(gtx, add, del, false) })
		},
		func(gtx layout.Context) layout.Dimensions {
			if prompt == "" {
				return layout.Dimensions{}
			}
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, prompt)
		})
}

// editedTitle is "Edited 2 files", "Created 1 file", or both.
func editedTitle(fs []fileChange) string {
	made := 0
	for _, f := range fs {
		if f.created {
			made++
		}
	}
	switch {
	case made == 0:
		return "Edited " + plural(len(fs), "file")
	case made == len(fs):
		return "Created " + plural(made, "file")
	}
	return "Edited " + plural(len(fs)-made, "file") + ", created " + strconv.Itoa(made)
}

// counts is +N −M, the green and red of a change; a new file without
// removed lines shows only +N.
func (d *cdraw) counts(gtx layout.Context, add, del int, created bool) layout.Dimensions {
	th := d.th
	f, sz := th.UIFont, th.Sp(theme.Small)
	ws := []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, f, sz, th.Readable(th.Green, th.Bg), "+"+strconv.Itoa(add))
	}}
	if del > 0 || !created {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, f, sz, th.Readable(th.Red, th.Bg), "−"+strconv.Itoa(del))
		})
	}
	return hrow(gtx, gtx.Dp(6), ws...)
}

// changeFileHead is a file's line over its diff: its path as a chip that
// opens the file, and its counts on the right.
func (d *cdraw) changeFileHead(gtx layout.Context, turn string, f fileChange) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(hit)
	ck := d.t.click("side:path:" + turn + ":" + f.path)
	for ck.Clicked(gtx) {
		d.t.showFile(f.path)
	}
	cc, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.counts(gtx, f.add, f.del, f.created) })
	at(gtx, image.Pt(w-cd.Size.X, (h-cd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { cc.Add(gtx.Ops); return cd })
	at(gtx, image.Pt(-gtx.Dp(6), (h-gtx.Dp(22))/2), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = max(0, w-cd.Size.X-gtx.Dp(theme.SpaceM))
		return d.linkChip(gtx, ck, f.path, true)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// changeDiff is f's diff in the diff box, its first head lines when head
// is not 0.
func (d *cdraw) changeDiff(gtx layout.Context, turn string, f fileChange, head int) layout.Dimensions {
	lines := changeLines(f)
	b := monoBox{id: "side:" + turn + ":" + f.path, lines: diffMono(lines), nums: 2, signs: true,
		copy: unifiedText(f.path, lines), copyLabel: ""}
	if f.created && len(f.diffs) == 1 {
		b.nums, b.copy = 1, newText(lines)
	}
	if head > 0 && len(b.lines) > head {
		b.lines, b.copy = b.lines[:head], ""
	}
	return d.box(gtx, b)
}

// sideNote is a quiet line at the top of an empty or refused view.
func (d *cdraw) sideNote(gtx layout.Context, ic, msg string) layout.Dimensions {
	th := d.th
	return layout.UniformInset(sidePad).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, gtx.Dp(14), th.Muted) },
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X -= gtx.Dp(22)
				return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, msg, 3)
			})
	})
}

// fileView is the file on disk: a line with its path and size, then its
// text with line numbers, its markdown, its picture, or why not.
func (t *Transcript) fileView(gtx layout.Context, d *cdraw, size image.Point) {
	th := d.th
	s := &t.side
	if s.pv == nil {
		d.sideNote(gtx, "file", "Click a file's path in the chat to read it here")
		return
	}
	pv := s.pv
	pad := gtx.Dp(sidePad)
	hh := gtx.Dp(hit + theme.SpaceS)
	// The path, its folder quieter, and the size on the right.
	meta := byteSize(pv.size)
	switch {
	case pv.cut:
		meta = "first " + byteSize(maxSource) + " of " + byteSize(pv.size)
	case pv.kind == previewPicture:
		b := pv.img.Bounds()
		meta = fmt.Sprintf("%d × %d · %s", b.Dx(), b.Dy(), byteSize(pv.size))
	case pv.kind == previewText:
		meta = plural(len(s.lines), "line") + " · " + meta
	case pv.kind == previewNone:
		meta = ""
	}
	mc, md := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, meta)
	})
	at(gtx, image.Pt(size.X-pad-md.Size.X, (hh-md.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { mc.Add(gtx.Ops); return md })
	shown := relPath(t.Dir, pv.path)
	at(gtx, image.Pt(pad, 0), func(gtx layout.Context) layout.Dimensions {
		room := max(0, size.X-2*pad-md.Size.X-gtx.Dp(theme.SpaceM))
		return d.pathLine(gtx, shown, room, hh, true)
	})
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, hh-1), Max: image.Pt(size.X, hh)}.Op())
	body := image.Pt(size.X, max(0, size.Y-hh))
	defer op.Offset(image.Pt(0, hh)).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(body)
	defer clip.Rect{Max: body}.Push(gtx.Ops).Pop()
	switch pv.kind {
	case previewNone:
		d.sideNote(gtx, "circle-alert", pv.why)
	case previewMD:
		s.md.Size = th.Sp(theme.Large) // reply prose's size, not the reader's
		s.md.Body(gtx, th)
	case previewPicture:
		t.pictureView(gtx, d, body)
	default:
		t.linesView(gtx, d, body)
	}
}

// pathLine is a path in one line, its folder quieter than its name, cut
// in the middle to room: the name in 500 as a title, else in a tool row's
// path color.
func (d *cdraw) pathLine(gtx layout.Context, p string, room, h int, title bool) layout.Dimensions {
	th := d.th
	sz := th.Sp(theme.Body)
	p = middleCut(p, room, gtx.Sp(sz)*55/100)
	dir, file := "", p
	if i := strings.LastIndexAny(p, `/\`); i >= 0 && i < len(p)-1 {
		dir, file = p[:i+1], p[i+1:]
	}
	lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = room
		return hrow(gtx, 0,
			func(gtx layout.Context) layout.Dimensions {
				if dir == "" {
					return layout.Dimensions{}
				}
				return d.lbl(gtx, th.UIFont, sz, th.Muted, dir)
			},
			func(gtx layout.Context) layout.Dimensions {
				if title {
					return d.lbl(gtx, medium(th.UIFont), sz, th.Fg, file)
				}
				return d.lbl(gtx, th.UIFont, sz, theme.Mix(th.Fg, th.Bg, 0.2), file)
			})
	})
	return at(gtx, image.Pt(0, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
}

// pictureView is a picture at its own pixels at most, centered at the
// top, on the surface.
func (t *Transcript) pictureView(gtx layout.Context, d *cdraw, size image.Point) {
	s := &t.side
	pad := gtx.Dp(sidePad)
	ib := s.img.Size()
	room := image.Pt(max(1, size.X-2*pad), max(1, size.Y-2*pad))
	scale := min(1, float32(room.X)/float32(max(1, ib.X)), float32(room.Y)/float32(max(1, ib.Y)))
	w, h := int(float32(ib.X)*scale), int(float32(ib.Y)*scale)
	p := image.Pt((size.X-w)/2, pad)
	r := image.Rectangle{Min: p, Max: p.Add(image.Pt(w, h))}
	rad := gtx.Dp(theme.RadiusControl)
	paint.FillShape(gtx.Ops, d.th.Surface, clip.UniformRRect(r, rad).Op(gtx.Ops))
	defer clip.UniformRRect(r, rad).Push(gtx.Ops).Pop()
	at(gtx, p, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		return widget.Image{Src: s.img, Fit: widget.ScaleDown, Position: layout.NW, Scale: 1 / gtx.Metric.PxPerDp}.Layout(gtx)
	})
}

// linesView is a text file in mono with its line numbers, a row per line
// so a 10,000-line file lays out only the lines in view, scrolled
// sideways as one.
func (t *Transcript) linesView(gtx layout.Context, d *cdraw, size image.Point) {
	th := d.th
	s := &t.side
	st := d.mono()
	_, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.MonoFont, st.size, th.Muted, "0")
	})
	cw := max(1, cd.Size.X)
	lh := int(float32(gtx.Sp(st.size))*st.lh + 0.5)
	numW := cw*len(strconv.Itoa(len(s.lines))) + gtx.Dp(theme.SpaceM+theme.SpaceS)
	textX := numW + gtx.Dp(theme.SpaceM)
	viewW := max(0, size.X-textX-gtx.Dp(theme.SpaceM))
	maxOff := max(0, s.long*cw-viewW)
	s.hoff = min(max(s.hoff, 0), maxOff)
	s.hoff += s.hs.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Horizontal, pointer.ScrollRange{Min: -s.hoff, Max: maxOff - s.hoff}, pointer.ScrollRange{})
	s.hoff = min(max(s.hoff, 0), maxOff)

	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Bg, 0.5), clip.Rect{Max: image.Pt(numW, size.Y)}.Op())
	top := gtx.Dp(theme.SpaceS)
	s.rows.Axis = layout.Vertical
	s.rows.Layout(gtx, len(s.lines)+2, func(gtx layout.Context, i int) layout.Dimensions {
		if i == 0 || i == len(s.lines)+1 {
			return layout.Dimensions{Size: image.Pt(size.X, top)}
		}
		i--
		no := strconv.Itoa(i + 1)
		nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.MonoFont, st.size, th.TextQuiet, no)
		})
		y := (lh - nd.Size.Y) / 2
		at(gtx, image.Pt(numW-gtx.Dp(theme.SpaceS)-nd.Size.X, y), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
		if l := s.lines[i]; l != "" {
			cl := clip.Rect{Min: image.Pt(textX-gtx.Dp(2), 0), Max: image.Pt(size.X, lh)}.Push(gtx.Ops)
			at(gtx, image.Pt(textX-s.hoff, y), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = 1 << 20
				return d.lbl(gtx, th.MonoFont, st.size, st.c, mdview.Detab(l))
			})
			cl.Pop()
		}
		return layout.Dimensions{Size: image.Pt(size.X, lh)}
	})
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	s.hs.Add(gtx.Ops)
	area.Pop()
	if maxOff > 0 && viewW > 0 {
		tw := max(gtx.Dp(24), viewW*viewW/(s.long*cw))
		x := textX + s.hoff*(viewW-tw)/maxOff
		bar := image.Rect(x, size.Y-gtx.Dp(4), x+tw, size.Y-gtx.Dp(1))
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.UniformRRect(bar, bar.Dy()/2).Op(gtx.Ops))
	}
}

// openOutside opens p as the chat's links did before the panel: in the
// editor or the desktop's app.
func (t *Transcript) openOutside(p string) {
	if t.Open != nil && p != "" {
		t.Open(p)
	}
}

// withSide lays chat out at the left of the side panel while the panel
// is open, and alone while it is shut.
func (t *Transcript) withSide(gtx layout.Context, th *theme.Theme, s *session.Snapshot, chat layout.Widget) layout.Dimensions {
	full := gtx.Constraints.Max
	pw := t.sideWidth(gtx, full.X)
	if pw == 0 {
		return chat(gtx)
	}
	if t.open == nil {
		t.init()
	}
	if cw := full.X - pw; cw > 0 {
		g := gtx
		g.Constraints = layout.Exact(image.Pt(cw, full.Y))
		chat(g)
	} else {
		t.now = gtx.Now
		t.blocks = blocks(s.Content, t.blocks)
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &t.focus, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			t.CloseFiles()
		}
	}
	if t.side.on {
		d := &cdraw{t: t, th: th, s: s, sec: gtx.Now.Sub(t.epoch).Seconds()}
		at(gtx, image.Pt(full.X-pw, 0), func(gtx layout.Context) layout.Dimensions {
			t.sidePanel(gtx, d, image.Pt(pw, full.Y))
			return layout.Dimensions{Size: image.Pt(pw, full.Y)}
		})
	}
	return layout.Dimensions{Size: full}
}
