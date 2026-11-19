package panel

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// toolThumbH is how tall a tool result's picture draws, at most.
const toolThumbH = 120

// zoomFull is the most pixels tall a picture decodes to show it large.
const zoomFull = 2400

// zoomed is the picture drawn large over the transcript, "" key for none.
type zoomed struct {
	key string
	a   session.Attachment
}

// toolImages are a call's pictures in a row, each as tall as fits
// toolThumbH, drawn at their own pixels; a click shows one large.
func (d *cdraw) toolImages(gtx layout.Context, it *session.Item) layout.Dimensions {
	imgs := it.Tool.Images
	gap := gtx.Dp(theme.SpaceS)
	each := (gtx.Constraints.Max.X - gap*(len(imgs)-1)) / max(len(imgs), 1)
	ws := make([]layout.Widget, len(imgs))
	for i := range imgs {
		a := imgs[i]
		ws[i] = func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = max(0, each)
			return d.toolThumb(gtx, it.ID+"/"+a.ID, a)
		}
	}
	return hrow(gtx, gap, ws...)
}

// toolThumb is one picture, rounded, with a ring; Hover lightens it.
func (d *cdraw) toolThumb(gtx layout.Context, k string, a session.Attachment) layout.Dimensions {
	th := d.th
	maxH := gtx.Dp(toolThumbH)
	tb := d.t.thumbs.snapshot(d.t.thumbs.getSized(k+"@"+strconv.Itoa(maxH), &a, maxH, nil))
	if tb.ready && tb.size == (image.Point{}) {
		return d.fileChip(gtx, "image", "Image")
	}
	h, w := maxH, gtx.Dp(160)
	if tb.ready {
		h = max(tb.size.Y, gtx.Dp(40)) // its own pixels, so it stays sharp, but big enough to see and click
		w = h * tb.size.X / max(tb.size.Y, 1)
	} else {
		d.t.live = true
	}
	w = max(min(w, gtx.Constraints.Max.X, gtx.Dp(360)), gtx.Dp(24))
	ck := d.t.click("img:" + k)
	for ck.Clicked(gtx) {
		d.t.tool.zoom = zoomed{key: k, a: a}
		gtx.Execute(key.FocusCmd{Tag: &d.t.focus})
	}
	return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(0, 0, w, h)
		rad := gtx.Dp(theme.RadiusPopover)
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(r, rad).Op(gtx.Ops))
		cl := clip.UniformRRect(r, rad).Push(gtx.Ops)
		if tb.ready {
			g := gtx
			g.Constraints = layout.Exact(r.Size())
			widget.Image{Src: tb.img, Fit: widget.Cover, Position: layout.Center}.Layout(g)
		}
		if ck.Hovered() {
			veil := th.Fg
			veil.A = 0x14
			paint.FillShape(gtx.Ops, veil, clip.Rect(r).Op())
		}
		pointer.CursorPointer.Add(gtx.Ops)
		cl.Pop()
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: clip.UniformRRect(r, rad).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		return layout.Dimensions{Size: r.Size()}
	})
}

// zoomLayer draws the zoomed picture over the transcript, as large as
// fits at its own pixels at most; a click or Esc closes it.
func (t *Transcript) zoomLayer(gtx layout.Context, d *cdraw, size image.Point) {
	z := &t.tool.zoom
	if z.key == "" {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &t.focus, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			z.key = ""
		}
	}
	ck := t.click("zoom:close")
	for ck.Clicked(gtx) {
		z.key = ""
	}
	if z.key == "" {
		return
	}
	th := d.th
	full := t.thumbs.snapshot(t.thumbs.getSized(z.key+"@full", &z.a, zoomFull, nil))
	img := full
	if !full.ready {
		t.live = true
		img = t.thumbs.snapshot(t.thumbs.getSized(z.key+"@"+strconv.Itoa(gtx.Dp(toolThumbH)), &z.a, gtx.Dp(toolThumbH), nil))
	}
	clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		scrim := th.Bg
		scrim.A = 0xf4
		paint.FillShape(gtx.Ops, scrim, clip.Rect{Max: size}.Op())
		icon(gtx, image.Pt(size.X-gtx.Dp(36), gtx.Dp(16)), "x", 20, th.Muted)
		if img.ready && img.size.X > 0 && img.size.Y > 0 {
			m := gtx.Dp(48)
			room := image.Pt(max(1, size.X-2*m), max(1, size.Y-2*m))
			s := min(float32(room.X)/float32(img.size.X), float32(room.Y)/float32(img.size.Y))
			if full.ready {
				s = min(s, 1)
			}
			w, h := int(float32(img.size.X)*s), int(float32(img.size.Y)*s)
			r := image.Rectangle{Min: image.Pt((size.X-w)/2, (size.Y-h)/2)}
			r.Max = r.Min.Add(image.Pt(w, h))
			rad := gtx.Dp(theme.RadiusPopover)
			cl := clip.UniformRRect(r, rad).Push(gtx.Ops)
			at(gtx, r.Min, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Exact(r.Size())
				return widget.Image{Src: img.img, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
			})
			cl.Pop()
			paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: clip.UniformRRect(r, rad).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		}
		return layout.Dimensions{Size: size}
	})
}

// linkRow is a row of a link list: an icon, a dim lead and the main
// text, a quiet note on the right; a click opens target.
type linkRow struct {
	icon, lead, main, note, target string
}

// fileList is the files a search found with their matches; a click opens
// one.
func (d *cdraw) fileList(gtx layout.Context, id string, hits []fileHit) layout.Dimensions {
	rows := make([]linkRow, len(hits))
	for i, h := range hits {
		p := relPath(d.t.Dir, h.path)
		dir, file := "", p
		if k := strings.LastIndexByte(p, '/'); k >= 0 {
			dir, file = p[:k+1], p[k+1:]
		}
		note := ""
		if h.n > 0 {
			note = strconv.Itoa(h.n)
		}
		rows[i] = linkRow{icon: "file", lead: dir, main: file, note: note, target: h.path}
	}
	return d.linkList(gtx, id, rows, "file")
}

// sourceList is a web search's pages, titled, with their domains.
func (d *cdraw) sourceList(gtx layout.Context, id string, src []webSource) layout.Dimensions {
	rows := make([]linkRow, len(src))
	for i, s := range src {
		t := strings.TrimSpace(s.Title)
		if t == "" {
			t = shortURL(s.URL)
		}
		rows[i] = linkRow{icon: "globe", main: t, note: host(s.URL), target: s.URL}
	}
	return d.linkList(gtx, id, rows, "source")
}

// linkList draws rows on a panel, the first listHead with Show all.
func (d *cdraw) linkList(gtx layout.Context, id string, rows []linkRow, noun string) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	all := d.t.open["all:"+id]
	shown := rows
	if !all && len(rows) > listHead {
		shown = rows[:listHead]
	}
	rh, pad := gtx.Dp(26), gtx.Dp(4)
	h := len(shown)*rh + 2*pad
	rad := gtx.Dp(theme.RadiusControl)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(0, 0, w, h), rad).Op(gtx.Ops))
	sz := th.Sp(theme.Small)
	for i, r := range shown {
		ck := d.t.click("row:" + id + ":" + strconv.Itoa(i))
		for ck.Clicked(gtx) {
			d.t.openTarget(r.target)
		}
		at(gtx, image.Pt(pad, pad+i*rh), func(gtx layout.Context) layout.Dimensions {
			rw := w - 2*pad
			return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
				box := image.Rect(0, 0, rw, rh)
				if ck.Hovered() {
					paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(box, gtx.Dp(5)).Op(gtx.Ops))
				}
				x := gtx.Dp(8)
				icon(gtx, image.Pt(x, (rh-gtx.Dp(12))/2), r.icon, 12, th.Muted)
				x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
				nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
					if r.note == "" {
						return layout.Dimensions{}
					}
					return d.lbl(gtx, th.UIFont, sz, th.Muted, r.note)
				})
				nx := rw - gtx.Dp(8) - nd.Size.X
				fg := theme.Mix(th.Fg, th.Bg, 0.12)
				if ck.Hovered() {
					fg = th.Fg
				}
				g := gtx
				g.Constraints.Max.X = max(0, nx-gtx.Dp(12)-x)
				tc, td := measure(g, func(gtx layout.Context) layout.Dimensions {
					return hrow(gtx, 0,
						func(gtx layout.Context) layout.Dimensions {
							if r.lead == "" {
								return layout.Dimensions{}
							}
							return d.lbl(gtx, th.UIFont, sz, th.TextQuiet, r.lead)
						},
						func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, th.UIFont, sz, fg, r.main) })
				})
				base := centerBase(rh, td)
				at(gtx, image.Pt(x, onBase(base, td)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
				if nd.Size.X > 0 {
					at(gtx, image.Pt(nx, onBase(base, nd)), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
				}
				defer clip.Rect(box).Push(gtx.Ops).Pop()
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: box.Size()}
			})
		})
	}
	if len(rows) <= listHead {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	label := "Show all " + plural(len(rows), noun)
	ic := "chevron-down"
	if all {
		label, ic = "Show the first "+strconv.Itoa(listHead), "chevron-right"
	}
	sk := d.t.click("all:" + id)
	for sk.Clicked(gtx) {
		d.t.open["all:"+id] = !all
	}
	md := at(gtx, image.Pt(0, h+gtx.Dp(gapShowAll)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, ic, label, false)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(gapShowAll)+md.Size.Y)}
}
