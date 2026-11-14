package app

import (
	"cmp"
	"context"
	"image"
	"strconv"
	"strings"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/skillshare"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ssCardsEvery is how often Machines reads each card while it shows.
const ssCardsEvery = time.Minute

// ssHosts are the machines the window is connected to: this one first,
// then each [[hosts]] machine that is up. A window of pitwall --host has
// that host alone.
func (u *ui) ssHosts() []string {
	if h, ok := u.b.(Hoster); ok {
		var out []string
		for _, hi := range h.Hosts() {
			if hi.Name == "" || hi.Up {
				out = append(out, hi.Name)
			}
		}
		return out
	}
	return []string{Host}
}

// ssLabel is a machine's name as the popup shows it.
func ssLabel(host string) string { return cmp.Or(host, "this computer") }

// ssReadCards reads each machine's card that is due, off the UI goroutine.
func (u *ui) ssReadCards() {
	s := &u.ss
	inv := u.invalidateFn()
	for _, host := range u.ssHosts() {
		m := s.machine(host)
		s.mu.Lock()
		due := !m.reading && time.Since(m.cardAt) >= ssCardsEvery
		if due {
			m.reading = true
		}
		s.mu.Unlock()
		if !due {
			continue
		}
		do := u.ssDo(ssTarget{host: host})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c := do(ctx, skillshare.Call{Op: skillshare.OpMachine}, nil).Machine
			s.mu.Lock()
			m.card, m.reading, m.cardAt = &c, false, time.Now()
			s.mu.Unlock()
			inv()
		}()
	}
}

// ssChecked is "Checked …" for the newest card, "" before any.
func (u *ui) ssChecked(now time.Time) string {
	s := &u.ss
	var at time.Time
	s.mu.Lock()
	for _, m := range s.machines {
		if m.card != nil && m.cardAt.After(at) {
			at = m.cardAt
		}
	}
	s.mu.Unlock()
	if at.IsZero() {
		return ""
	}
	return "Checked " + sidebar.RelTime(now, at)
}

// ssMachineRows is Machines: a card per machine.
func (u *ui) ssMachineRows(st *model.State, busy bool) []ssW {
	var rows []ssW
	for i, host := range u.ssHosts() {
		if i > 0 {
			rows = append(rows, ssSpace(10))
		}
		rows = append(rows, u.ssCard(st, host, busy))
	}
	return rows
}

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/main.js
// (renderMachines).

// ssCard is one machine: its name, version and health, a line from the
// remote into the source and one from the source into the targets, and
// Pull and sync, or Sync both ways when it has work of its own. A click on
// its name shows it in Overview.
func (u *ui) ssCard(st *model.State, host string, busy bool) ssW {
	th, s := u.th, &u.ss
	m := s.machine(host)
	s.mu.Lock()
	var c *skillshare.Machine
	if m.card != nil {
		cc := *m.card
		c = &cc
	}
	s.mu.Unlock()
	t := ssTarget{host: host}
	id := "m:" + host
	s.acts[id+":open"] = func() { u.ssPickHost(st, host); s.library = false }
	ok := c != nil && !c.Missing && c.Err == ""
	attention := ok && (c.Git.Late() || c.Drift > 0)
	var acts []gl.Widget
	if ok {
		op, label := skillshare.OpPull, "Pull and sync"
		if c.Git.BothWays() {
			op, label = skillshare.OpBothWays, "Sync both ways"
		}
		kind := kit.Secondary
		if attention {
			kind = kit.Primary
		}
		acts = append(acts, u.ssButton(id+":pull", kind.When(!busy && c.Git.Upstream != ""), label, func() {
			u.ssRunOn(t, label+"…", func(ctx context.Context, do skillshare.Do, step func(string)) skillshare.Outcome {
				return do(ctx, skillshare.Call{Op: op}, step).Outcome
			})
		}))
		if c.Latest != "" {
			to := strings.TrimPrefix(c.Latest, "v")
			acts = append(acts, u.ssButton(id+":upgrade", kit.Secondary.When(!busy), "Update to "+to, func() {
				u.ssRunOn(t, "Updating skillshare…", func(ctx context.Context, do skillshare.Do, step func(string)) skillshare.Outcome {
					return do(ctx, skillshare.Call{Op: skillshare.OpUpgrade}, step).Outcome
				})
			}))
		}
	}
	return func(gtx gl.Context) gl.Dimensions {
		w := gtx.Constraints.Max.X
		px := gtx.Dp(12)
		macro := op.Record(gtx.Ops)
		y := 0
		inner := func(h int, f func(gtx gl.Context)) {
			o := op.Offset(image.Pt(px, y)).Push(gtx.Ops)
			g := gtx
			g.Constraints = gl.Exact(image.Pt(w-2*px, h))
			f(g)
			o.Pop()
			y += h
		}
		// The name row: a dot, the name, "this computer", the version.
		headH := gtx.Dp(40)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w, headH))
		s.click(id+":open").Layout(g, func(gtx gl.Context) gl.Dimensions {
			if s.click(id + ":open").Hovered() {
				paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Max}, gtx.Dp(10)).Op(gtx.Ops))
			}
			return gl.Dimensions{Size: gtx.Constraints.Max}
		})
		inner(headH, func(gtx gl.Context) {
			dot, right, rc := th.Muted, "checking…", th.Muted
			switch {
			case c == nil:
			case c.Err != "":
				dot, right = th.Red, ""
			case c.Missing:
				right = "skillshare not found"
			default:
				dot, right = th.Green, "v"+strings.TrimPrefix(c.Version, "v")
				if attention {
					dot = th.Yellow
				}
				if c.Latest != "" {
					right += " · update"
					rc = th.Readable(th.Blue, th.Surface)
				}
			}
			kids := []gl.FlexChild{
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					r := gtx.Dp(4)
					h := gtx.Constraints.Max.Y
					paint.FillShape(gtx.Ops, dot, clip.Ellipse(image.Rect(0, h/2-r, 2*r, h/2+r)).Op(gtx.Ops))
					return gl.Dimensions{Size: image.Pt(2*r, h)}
				}),
				gl.Rigid(ssGap(10)),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, ssLabel(host))
				}),
			}
			kids = append(kids, gl.Flexed(1, ssFill), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), rc, right)
			}))
			gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
		})
		switch {
		case c != nil && c.Err != "":
			inner(0, func(gtx gl.Context) {})
			d := func() gl.Dimensions {
				o := op.Offset(image.Pt(px, y)).Push(gtx.Ops)
				defer o.Pop()
				g := gtx
				g.Constraints = gl.Constraints{Max: image.Pt(w-2*px, gtx.Constraints.Max.Y)}
				return para(g, th, th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), c.Err)
			}()
			y += d.Size.Y + gtx.Dp(12)
		case ok:
			lane := func(from, to, state string, warn bool) {
				inner(gtx.Dp(26), func(gtx gl.Context) {
					h := gtx.Constraints.Max.Y
					fc, fsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, from)
					o := op.Offset(image.Pt(0, (h-fsz.Y)/2)).Push(gtx.Ops)
					fc.Add(gtx.Ops)
					o.Pop()
					x := gtx.Dp(56)
					line := th.BorderSubtle
					dot := th.Green
					if warn {
						line, dot = th.Yellow, th.Yellow
					}
					paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(x, h/2), Max: image.Pt(x+gtx.Dp(36), h/2+1)}.Op())
					x += gtx.Dp(42)
					r := gtx.Dp(4)
					paint.FillShape(gtx.Ops, dot, clip.Ellipse(image.Rect(x, h/2-r, x+2*r, h/2+r)).Op(gtx.Ops))
					x += 2*r + gtx.Dp(8)
					tc, tsz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, to)
					o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
					tc.Add(gtx.Ops)
					o.Pop()
					sc := th.Muted
					if warn {
						sc = th.Readable(th.Yellow, th.Surface)
					}
					rc, rsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), sc, state)
					o = op.Offset(image.Pt(gtx.Constraints.Max.X-rsz.X, (h-rsz.Y)/2)).Push(gtx.Ops)
					rc.Add(gtx.Ops)
					o.Pop()
				})
			}
			drift := "in sync"
			if c.Drift > 0 {
				drift = strconv.Itoa(c.Drift) + " to sync"
			}
			lane("Remote", "Source", c.Git.Line(), c.Git.Late())
			lane("Source", plural(c.Targets, "target"), drift, c.Drift > 0)
			y += gtx.Dp(6)
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
			inner(gtx.Dp(44), func(gtx gl.Context) {
				kids := []gl.FlexChild{gl.Rigid(func(gtx gl.Context) gl.Dimensions { return ssButtons(gtx, acts...) }), gl.Flexed(1, ssFill)}
				if c.Errors > 0 || c.Warnings > 0 {
					text, col := "doctor · "+plural(c.Errors, "error"), th.Readable(th.Red, th.Surface)
					if c.Errors == 0 {
						text, col = "doctor · "+plural(c.Warnings, "warning"), th.Muted
					}
					kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), col, text)
					}))
				}
				gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
			})
		}
		call := macro.Stop()
		box := image.Rect(0, 0, w, y)
		rr := gtx.Dp(10)
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.UniformRRect(box, rr).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box.Inset(1), rr-1).Op(gtx.Ops))
		call.Add(gtx.Ops)
		return gl.Dimensions{Size: box.Max}
	}
}

// ssMachinePill is the header's machine: its name, and with several
// machines a picker of them under it. It draws nothing in a window of
// this machine alone.
func (u *ui) ssMachinePill(gtx gl.Context, st *model.State) gl.Dimensions {
	th, s := u.th, &u.ss
	hosts := u.ssHosts()
	if len(hosts) < 2 && s.to.host == "" {
		return gl.Dimensions{}
	}
	if len(hosts) < 2 {
		return u.ssPill(gtx, ssLabel(s.to.host), th.Muted)
	}
	s.acts["machine"] = func() { s.menu = !s.menu }
	d := s.click("machine").Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return u.ssPill(gtx, ssLabel(s.to.host)+" ▾", th.Fg)
	})
	if !s.menu {
		return d
	}
	macro := op.Record(gtx.Ops)
	o := op.Offset(image.Pt(0, d.Size.Y+gtx.Dp(4))).Push(gtx.Ops)
	rowH, w := gtx.Dp(30), gtx.Dp(200)
	box := image.Rect(0, 0, w, rowH*len(hosts)+gtx.Dp(8))
	rr := gtx.Dp(8)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(box, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box.Inset(1), rr-1).Op(gtx.Ops))
	for i, host := range hosts {
		key := "machine:" + host
		s.acts[key] = func() { u.ssPickHost(st, host) }
		ro := op.Offset(image.Pt(gtx.Dp(4), gtx.Dp(4)+i*rowH)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w-gtx.Dp(8), rowH))
		c := s.click(key)
		c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			sz := gtx.Constraints.Max
			if c.Hovered() {
				paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rectangle{Max: sz}, gtx.Dp(6)).Op(gtx.Ops))
			}
			col := th.Fg
			if host == s.to.host {
				col = th.Primary
			}
			tc, tsz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), col, ssLabel(host))
			to := op.Offset(image.Pt(gtx.Dp(10), (sz.Y-tsz.Y)/2)).Push(gtx.Ops)
			tc.Add(gtx.Ops)
			to.Pop()
			return gl.Dimensions{Size: sz}
		})
		ro.Pop()
	}
	o.Pop()
	op.Defer(gtx.Ops, macro.Stop())
	return d
}
