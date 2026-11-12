package mobile

import (
	"context"
	"image"
	"io"
	"path"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// readerView is a markdown file of a tab's, full screen over the chat,
// read from the computer and drawn by the desktop's reader.
type readerView struct {
	v       *mdview.View // nil while no file is open
	machine string
	back    widget.Clickable
	copy    string // a link tapped in the file, copied next frame
}

// openFile shows path of tab, on machine, from its heading frag.
func (a *App) openFile(machine, tab, p, frag string) {
	c := a.fleet.Client(machine)
	if c == nil {
		return
	}
	a.closeReader()
	r := &a.reader
	// why: a View per file, so its loads still running read the Fetch they began with.
	r.v = &mdview.View{
		Invalidate: a.w.Invalidate,
		Size:       replySize,
		Fetch: func(ctx context.Context, name string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return c.File(ctx, tab, name)
		},
	}
	r.v.OpenURL = func(u string) { r.copy = u }
	r.machine = machine
	r.v.Open(p, frag)
}

func (a *App) closeReader() {
	if a.reader.v != nil {
		a.reader.v.Close()
	}
	a.reader = readerView{}
}

// readerBack goes back through the files the reader's links opened, then
// to the chat.
func (a *App) readerBack() {
	if a.reader.v.HasBack() {
		a.reader.v.Back()
		return
	}
	a.closeReader()
}

// readerLayout draws the reader. It draws in real dp, as the chat does.
func (a *App) readerLayout(gtx C) {
	gtx.Metric.PxPerDp /= zoom
	gtx.Metric.PxPerSp /= zoom
	r, th := &a.reader, a.th
	if r.back.Clicked(gtx) {
		a.readerBack()
		if r.v == nil {
			return
		}
	}
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return readerHeader(gtx, th, &r.back, r.v.Path()) }),
		layout.Flexed(1, func(gtx C) D {
			r.v.Body(gtx, th)
			return D{Size: gtx.Constraints.Max}
		}),
	)
	if r.copy != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(r.copy))})
		r.copy = ""
	}
}

// readerHeader is Back, the file's name and its folder, as the chat's
// header has Back and the tab's name.
func readerHeader(gtx C, th *theme.Theme, back *widget.Clickable, p string) D {
	h := gtx.Dp(64)
	name, dir := path.Base(p), path.Dir(p)
	d := layout.Inset{Left: 4, Right: 12}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, h)
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return roundButton(gtx, th, back, icBack, 24, th.Fg) }),
			layout.Flexed(1, func(gtx C) D {
				return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 18, th.Fg, 1, name)
						}),
						layout.Rigid(func(gtx C) D {
							if dir == "." {
								return D{}
							}
							return layout.Inset{Top: 3}.Layout(gtx, func(gtx C) D {
								return label(gtx, th, th.UIFont, 14, th.Muted, 1, dir)
							})
						}),
					)
				})
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// fileOpener is OpenFile for the chat of tab on machine l, nil when its
// computer serves no files.
func (a *App) fileOpener(l *link, tab string) func(p, frag string) {
	if !l.hello.Files {
		return nil
	}
	m := l.m.ID
	return func(p, frag string) { a.openFile(m, tab, p, frag) }
}
