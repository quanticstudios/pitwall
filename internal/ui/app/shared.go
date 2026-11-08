package app

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Shared sessions (docs/remote.md): a session whose tabs are on this
// machine and on [[hosts]] with window = true. Each tab and its panes are
// on one host. A remote pane gets a tag with its host's name hanging from
// its top border and, focused, a ring in the host's color.

// hostColor is host's color, Primary for this machine.
func (u *ui) hostColor(host string) color.NRGBA {
	return sidebar.HostColor(u.th, u.hosts, host)
}

// hostDown reports whether host is another machine that is not connected.
func (u *ui) hostDown(host string) bool { return sidebar.HostDown(u.hosts, host) }

// onHost is how the window shows host: the color of its tag text on solid,
// its solid fill, and its tint.
func (u *ui) onHost(host string, down bool) (on, solid, tint color.NRGBA) {
	c := u.hostColor(host)
	if down {
		c = u.th.Red
	}
	solid = u.th.Readable(c, u.th.TermBg)
	on = u.th.TermBg
	if theme.Contrast(on, solid) < theme.Contrast(color.NRGBA{R: 255, G: 255, B: 255, A: 255}, solid) {
		on = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	return on, solid, theme.Mix(u.th.TermBg, c, 0.14)
}

// tagPad is the room a remote pane's tag takes above its terminal: a
// row and a half.
func tagPad(cell image.Point) int { return cell.Y * 3 / 2 }

// remoteTag draws pane id's host tag at the top right of frame, and its
// details while the pointer is on it.
func (u *ui) remoteTag(gtx gl.Context, st *model.State, p *paneUI, id, host string, frame image.Rectangle, lit, down bool) {
	th := u.th
	on, solid, tint := u.onHost(host, down)
	label := host
	if down {
		label += " offline"
	}
	fg, bg := solid, tint
	if lit {
		fg, bg = on, solid
	}
	txt, ts := textCall(gtx, th, th.MonoFont, th.Sp(theme.Small), fg, label)
	is, gap, padX, h := gtx.Dp(11), gtx.Dp(4), gtx.Dp(7), gtx.Dp(19)
	w := padX + is + gap + ts.X + padX
	at := image.Pt(frame.Max.X-gtx.Dp(14)-w, frame.Min.Y)
	r := image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}
	rad := gtx.Dp(5)
	paint.FillShape(gtx.Ops, bg, clip.RRect{Rect: r, SE: rad, SW: rad}.Op(gtx.Ops))
	o := op.Offset(image.Pt(at.X+padX, at.Y+(h-is)/2)).Push(gtx.Ops)
	sidebar.Icon(gtx, "server", is, fg)
	o.Pop()
	o = op.Offset(image.Pt(at.X+padX+is+gap, at.Y+(h-ts.Y)/2)).Push(gtx.Ops)
	txt.Add(gtx.Ops)
	o.Pop()
	hov := clip.Rect(r).Push(gtx.Ops)
	p.tag.Layout(gtx, func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: r.Size()} })
	hov.Pop()
	if p.tag.Hovered() {
		u.tagTip(gtx, st, id, host, image.Pt(r.Max.X, r.Max.Y+gtx.Dp(6)), down)
	}
}

// tagRows are the details a host tag shows: the ssh destination, then
// the pane's folder and the latency, or while the host is down the error,
// the next try and when the screen is from.
func tagRows(h sidebar.HostInfo, folder string, down bool, now time.Time) [][2]string {
	rows := [][2]string{{"ssh", cmp.Or(h.SSH, h.Name)}}
	if !down {
		lat := "measuring"
		if h.Latency > 0 {
			lat = fmt.Sprintf("%d ms", h.Latency.Milliseconds())
		}
		return append(rows, [2]string{"folder", folder}, [2]string{"latency", lat})
	}
	rows = append(rows, [2]string{"error", cmp.Or(errLine(h.Note), "disconnected")})
	if !h.Retry.IsZero() {
		rows = append(rows, [2]string{"retry", "in " + retryIn(h.Retry, now)})
	} else {
		rows = append(rows, [2]string{"retry", "now"})
	}
	if !h.Since.IsZero() {
		rows = append(rows, [2]string{"screen", "from " + h.Since.Format("15:04")})
	}
	return rows
}

// errLine is a link note's error, without the "Trying again in" tail.
func errLine(note string) string {
	note, _, _ = strings.Cut(note, "\n")
	if i := strings.Index(note, ". Trying again in"); i >= 0 {
		note = note[:i]
	}
	if strings.HasSuffix(note, "…") {
		return ""
	}
	return note
}

// retryIn is how long until at, in whole seconds, at least 1s.
func retryIn(at, now time.Time) string {
	return fmt.Sprintf("%ds", max(1, int(at.Sub(now).Round(time.Second).Seconds())))
}

// tagTip draws a tag's details under its right edge at.
func (u *ui) tagTip(gtx gl.Context, st *model.State, pane, host string, at image.Point, down bool) {
	th := u.th
	h, _ := sidebar.FindHost(u.hosts, host)
	folder := ""
	if pn := findPane(st, pane); pn != nil {
		folder = pn.Cwd
	}
	rows := tagRows(h, folder, down, gtx.Now)
	if down {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second)})
	}
	type cell struct {
		c  op.CallOp
		sz image.Point
	}
	var ks, vs []cell
	kw, vw, rh := 0, 0, 0
	for _, r := range rows {
		vc := th.Fg
		if r[0] == "error" {
			vc = th.Readable(th.Red, th.SurfaceElevated)
		}
		k, ksz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, r[0])
		v, vsz := textCall(gtx, th, th.MonoFont, th.Sp(theme.Small), vc, r[1])
		ks, vs = append(ks, cell{k, ksz}), append(vs, cell{v, vsz})
		kw, vw, rh = max(kw, ksz.X), max(vw, vsz.X), max(rh, ksz.Y, vsz.Y)
	}
	pad, colGap, rowGap := gtx.Dp(10), gtx.Dp(14), gtx.Dp(3)
	box := image.Pt(2*pad+kw+colGap+vw, 2*gtx.Dp(8)+len(rows)*rh+(len(rows)-1)*rowGap)
	defer op.Offset(image.Pt(at.X-box.X, at.Y)).Push(gtx.Ops).Pop()
	kit.Surface(gtx, image.Rectangle{Max: box}, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderSubtle, th.SurfaceElevated)
	y := gtx.Dp(8)
	for i := range rows {
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		ks[i].c.Add(gtx.Ops)
		o.Pop()
		o = op.Offset(image.Pt(pad+kw+colGap, y)).Push(gtx.Ops)
		vs[i].c.Add(gtx.Ops)
		o.Pop()
		y += rh + rowGap
	}
}

// drawOffline is the line over a down host's focused pane: typing is off,
// the screen is the last one the window had, and Retry now tries at once.
func (u *ui) drawOffline(gtx gl.Context, p *paneUI, host string, frame image.Rectangle) {
	th := u.th
	if p.retry.Clicked(gtx) {
		if l, ok := u.b.(Linker); ok {
			l.Reconnect()
		}
	}
	h, _ := sidebar.FindHost(u.hosts, host)
	bold, bs := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, "Typing is off until "+host+" reconnects.")
	dim := ""
	if !h.Since.IsZero() {
		dim = "Screen from " + h.Since.Format("15:04") + "."
	}
	dimc, ds := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, dim)
	m := op.Record(gtx.Ops)
	bd := kit.Button(gtx, th, &p.retry, kit.Secondary, kit.Small, "Retry now")
	btn := m.Stop()
	is, gap := gtx.Dp(14), gtx.Dp(8)
	side := gtx.Dp(12)
	box := image.Rect(frame.Min.X+side, 0, frame.Max.X-side, 0)
	hgt := max(bs.Y, bd.Size.Y) + 2*gtx.Dp(6)
	box.Max.Y = frame.Max.Y - side
	box.Min.Y = box.Max.Y - hgt
	if box.Dx() < is+bs.X+bd.Size.X+4*gap {
		return
	}
	defer op.Offset(box.Min).Push(gtx.Ops).Pop()
	sz := box.Size()
	kit.Surface(gtx, image.Rectangle{Max: sz}, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderSubtle, th.SurfaceElevated)
	// why: presses on the line must not reach the terminal under it.
	area := clip.Rect{Max: sz}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.offBox)
	area.Pop()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &p.offBox, Kinds: pointer.Press}); !ok {
			break
		}
	}
	x := gtx.Dp(12)
	o := op.Offset(image.Pt(x, (sz.Y-is)/2)).Push(gtx.Ops)
	sidebar.Icon(gtx, "server", is, th.Readable(th.Red, th.SurfaceElevated))
	o.Pop()
	x += is + gap
	o = op.Offset(image.Pt(x, (sz.Y-bs.Y)/2)).Push(gtx.Ops)
	bold.Add(gtx.Ops)
	o.Pop()
	x += bs.X + gap
	if x+ds.X+gap+bd.Size.X+gtx.Dp(6) <= sz.X {
		o = op.Offset(image.Pt(x, (sz.Y-ds.Y)/2)).Push(gtx.Ops)
		dimc.Add(gtx.Ops)
		o.Pop()
	}
	o = op.Offset(image.Pt(sz.X-bd.Size.X-gtx.Dp(6), (sz.Y-bd.Size.Y)/2)).Push(gtx.Ops)
	btn.Add(gtx.Ops)
	o.Pop()
}

// pasteAskWindow is how long after focus moves to a pane of another host
// a paste of several lines into it asks first.
const pasteAskWindow = 15 * time.Second

// pasteAsks reports whether a paste of text into pane, on host, asks
// first: it has more than one line, pane is on another machine, and focus
// came to pane from a pane of another host within pasteAskWindow.
func pasteAsks(text, host, pane string, moved hostMove, now time.Time) bool {
	lines := strings.Contains(strings.TrimRight(text, "\r\n"), "\n")
	return lines && host != "" && moved.pane == pane && now.Sub(moved.at) < pasteAskWindow
}

// hostMove is the last time focus moved to a pane on another host than
// the pane it left.
type hostMove struct {
	pane string
	at   time.Time
}

// noteFocus records a focus move between hosts, for pasteAsks.
func (u *ui) noteFocus(now time.Time) {
	f := u.nav.focused()
	if f == "" || f == u.lastFocus {
		return
	}
	if u.lastFocus != "" && u.hostOf(f) != u.hostOf(u.lastFocus) {
		u.moved = hostMove{pane: f, at: now}
	}
	u.lastFocus = f
}

// pasteBody is the dialog that asks before a paste goes to another host.
func (u *ui) pasteBody(gtx gl.Context) gl.Dimensions {
	th := u.th
	host := u.hostOf(u.modal.ws)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Paste into a pane on "+host+"?")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "Focus just moved here from another machine. The text has several lines, and each line may run as a command on "+host+".")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Paste on "+host, kit.Primary)
		}),
	)
}
