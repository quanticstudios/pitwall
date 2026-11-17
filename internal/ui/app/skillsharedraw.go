package app

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/skillshare"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ssW is a widget of the popup's list.
type ssW = gl.Widget

// drawSkillshare draws the popup over the dimmed window: a header, the
// Overview and Library tabs with the scope switch, the shown view, and a
// status line while a command runs or after it ends.
func (u *ui) drawSkillshare(gtx gl.Context, st *model.State, v skillshare.View) {
	m, s, th := &u.modal, &u.ss, u.th
	size := gtx.Constraints.Max
	t := anim.At(gtx, m.openedAt, anim.Dialog)
	paint.FillShape(gtx.Ops, scrim(th, t), clip.Rect{Max: size}.Op())
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.backdrop)
	bg.Pop()
	if m.focus {
		m.focus = false
		gtx.Execute(key.FocusCmd{Tag: &m.backdrop})
	}
	w := min(gtx.Dp(720), size.X-gtx.Dp(32))
	h := min(gtx.Dp(640), size.Y-gtx.Dp(32))
	defer u.overlayCard(gtx, t, w, h, &m.body)()
	pad := gtx.Dp(20)
	headH, tabsH := gtx.Dp(52), gtx.Dp(40)
	busy := s.running()
	s.mu.Lock()
	note, noteAt := s.note, s.at
	s.mu.Unlock()
	statusH := 0
	if busy != "" || note.Text != "" && time.Since(noteAt) < ssNoteFor {
		statusH = gtx.Dp(36)
		if busy == "" {
			gtx.Execute(op.InvalidateCmd{At: noteAt.Add(ssNoteFor)})
		}
	}

	at := func(y int, hh int, f func(gtx gl.Context)) {
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w-2*pad, hh))
		f(g)
		o.Pop()
	}
	at(0, headH, func(gtx gl.Context) { u.ssHeader(gtx, st, v) })
	y := headH
	if s.sel == "" && s.inst == nil {
		at(y, tabsH, func(gtx gl.Context) { u.ssTabs(gtx, v) })
		y += tabsH
	}
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
	y++

	if _, old := u.ssOn(s.to.host); old && !s.machView {
		rows := []ssW{u.ssText(th.UIFont, th.Sp(theme.Body), th.Readable(th.Red, th.Surface), ssOld(s.to.host))}
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w-2*pad, h-y))
		u.ssList(g, rows)
		o.Pop()
		return
	}
	var rows []ssW
	switch {
	case s.inst != nil:
		rows = u.ssInstallRows(gtx, v)
	case s.sel != "":
		rows = u.ssPreviewRows(st, v)
	case s.machView:
		rows = u.ssMachineRows(st, busy != "")
	case s.library:
		rows = u.ssLibraryRows(v, busy != "")
	default:
		rows = u.ssOverviewRows(v, busy != "")
	}
	bodyH := h - y - statusH
	o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	cl := clip.Rect{Max: image.Pt(w-2*pad, bodyH)}.Push(gtx.Ops)
	g := gtx
	g.Constraints = gl.Exact(image.Pt(w-2*pad, bodyH))
	s.list.Axis = gl.Vertical
	u.ssList(g, rows)
	cl.Pop()
	o.Pop()
	if statusH > 0 {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, h-statusH), Max: image.Pt(w, h-statusH+1)}.Op())
		at(h-statusH, statusH, func(gtx gl.Context) { u.ssStatus(gtx, busy, note) })
	}
}

// ssList draws rows in the popup's scrolled list, with a gap above and
// below.
func (u *ui) ssList(gtx gl.Context, rows []ssW) {
	s := &u.ss
	gap := gtx.Dp(12)
	all := append(append([]ssW{func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(0, gap)} }}, rows...),
		func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(0, gap)} })
	s.list.Layout(gtx, len(all), func(gtx gl.Context, i int) gl.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return all[i](gtx)
	})
}

// ssHeader is the mark and title, the machine and the folder read, the
// CLI's version, and at the right Esc and the close button.
func (u *ui) ssHeader(gtx gl.Context, st *model.State, v skillshare.View) {
	th, s := u.th, &u.ss
	where := "global"
	if v.Project != "" {
		where = v.Project
		if s.to.host == "" && Host == "" {
			where = shortHome(where) // why: a remote machine's home is not this one's
		}
	}
	s.acts["close"] = u.closeSkillshare
	gl.Flex{Alignment: gl.Middle}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return sidebar.Icon(gtx, "skillshare", gtx.Dp(20), th.Primary)
		}),
		gl.Rigid(ssGap(10)),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "skillshare")
		}),
		gl.Rigid(ssGap(10)),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			d := u.ssMachinePill(gtx, st)
			if d.Size.X > 0 {
				d.Size.X += gtx.Dp(6)
			}
			return d
		}),
		gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = 0
			return u.ssPill(gtx, where, th.Muted)
		}),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			if v.Version == "" {
				return gl.Dimensions{}
			}
			return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "v"+strings.TrimPrefix(v.Version, "v"))
		}),
		gl.Rigid(ssGap(12)),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			call, sz := keycap(gtx, th, "Esc")
			call.Add(gtx.Ops)
			return gl.Dimensions{Size: sz}
		}),
		gl.Rigid(ssGap(6)),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, s.click("close"), kit.Ghost, kit.Small, func(gtx gl.Context, c color.NRGBA, sz int) { sidebar.Icon(gtx, "x", sz, c) })
		}),
	)
}

// shortHome is p with the home folder as ~.
func shortHome(p string) string {
	if h, _ := os.UserHomeDir(); h != "" {
		if rest, ok := strings.CutPrefix(p, h); ok && (rest == "" || rest[0] == filepath.Separator) {
			return "~" + rest
		}
	}
	return p
}

// ssTabs is Overview and Library, and the All, Project, Global switch when
// the folder is a skillshare project.
func (u *ui) ssTabs(gtx gl.Context, v skillshare.View) {
	th, s := u.th, &u.ss
	tab := func(id, label string, on bool, act func()) gl.FlexChild {
		s.acts[id] = act
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return s.click(id).Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				col, f := th.Muted, medium(th.UIFont)
				if on {
					col, f = th.Fg, semibold(th.UIFont)
				}
				call, sz := textCall(gtx, th, f, th.Sp(theme.Body), col, label)
				h := gtx.Constraints.Max.Y
				o := op.Offset(image.Pt(0, (h-sz.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				o.Pop()
				if on {
					paint.FillShape(gtx.Ops, th.Fg, clip.Rect{Min: image.Pt(0, h-gtx.Dp(2)), Max: image.Pt(sz.X, h)}.Op())
				}
				return gl.Dimensions{Size: image.Pt(sz.X, h)}
			})
		})
	}
	kids := []gl.FlexChild{
		tab("tab-overview", "Overview", !s.library && !s.machView, func() { s.library, s.machView = false, false; s.list.Position = gl.Position{} }),
		gl.Rigid(ssGap(20)),
		tab("tab-library", "Library", s.library && !s.machView, func() { s.library, s.machView = true, false; s.list.Position = gl.Position{} }),
		gl.Rigid(ssGap(20)),
		tab("tab-machines", "Machines", s.machView, func() { s.machView = true; s.list.Position = gl.Position{} }),
		gl.Flexed(1, ssFill),
	}
	if s.machView {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, u.ssChecked(gtx.Now))
		}))
	} else if v.Project == "" {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "Global")
		}))
	} else {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.ssSegmented(gtx, "scope", [][2]string{{"all", "All"}, {skillshare.Project, "Project"}, {skillshare.Global, "Global"}}, s.scope, func(k string) { s.scope = k })
		}))
	}
	gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
}

// ssSegmented is a row of choices, the picked one filled.
func (u *ui) ssSegmented(gtx gl.Context, id string, opts [][2]string, picked string, pick func(string)) gl.Dimensions {
	th, s := u.th, &u.ss
	h, px := gtx.Dp(26), gtx.Dp(10)
	m := op.Record(gtx.Ops)
	x := gtx.Dp(2)
	for _, o := range opts {
		key := id + ":" + o[0]
		s.acts[key] = func() { pick(o[0]) }
		on := o[0] == picked
		call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Small), ssPick2(on, th.Fg, th.Muted), o[1])
		bw := sz.X + 2*px
		off := op.Offset(image.Pt(x, gtx.Dp(2))).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(bw, h-gtx.Dp(4)))
		s.click(key).Layout(g, func(gtx gl.Context) gl.Dimensions {
			r := image.Rectangle{Max: gtx.Constraints.Max}
			if on {
				paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(r, gtx.Dp(5)).Op(gtx.Ops))
			}
			o := op.Offset(image.Pt(px, (r.Dy()-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			return gl.Dimensions{Size: r.Max}
		})
		off.Pop()
		x += bw
	}
	call := m.Stop()
	box := image.Rect(0, 0, x+gtx.Dp(2), h)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(box, gtx.Dp(7)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return gl.Dimensions{Size: box.Max}
}

func ssPick2(on bool, a, b color.NRGBA) color.NRGBA {
	if on {
		return a
	}
	return b
}

// ssLine draws one line of text and returns its size.
func (u *ui) ssLine(gtx gl.Context, f font.Font, size unit.Sp, c color.NRGBA, s string) gl.Dimensions {
	call, sz := textCall(gtx, u.th, f, size, c, s)
	call.Add(gtx.Ops)
	return gl.Dimensions{Size: sz}
}

// ssFill takes the room a Flexed child is given.
func ssFill(gtx gl.Context) gl.Dimensions {
	return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
}

func ssGap(dp unit.Dp) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(gtx.Dp(dp), gtx.Dp(dp))} }
}

// ssPill is muted text on a filled tag.
func (u *ui) ssPill(gtx gl.Context, text string, c color.NRGBA) gl.Dimensions {
	th := u.th
	px, h := gtx.Dp(8), gtx.Dp(22)
	g := gtx
	g.Constraints.Max.X = max(gtx.Constraints.Max.X-2*px, 0)
	call, sz := textCall(g, th, th.UIFont, th.Sp(theme.Small), c, text)
	box := image.Rect(0, 0, sz.X+2*px, h)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(box, h/2).Op(gtx.Ops))
	o := op.Offset(image.Pt(px, (h-sz.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: box.Max}
}

// ssButton is a kit button whose click runs act.
func (u *ui) ssButton(id string, k kit.Kind, label string, act func()) gl.Widget {
	s := &u.ss
	s.acts[id] = act
	return func(gtx gl.Context) gl.Dimensions {
		return kit.Button(gtx, u.th, s.click(id), k, kit.Small, label)
	}
}

// ssButtons lays buttons out in a row from the left.
func ssButtons(gtx gl.Context, ws ...gl.Widget) gl.Dimensions {
	var kids []gl.FlexChild
	for i, w := range ws {
		if i > 0 {
			kids = append(kids, gl.Rigid(ssGap(8)))
		}
		kids = append(kids, gl.Rigid(w))
	}
	return gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
}

// ssColor is a row's dot: yellow while it waits, blue for an update,
// green in sync, muted when off or unknown.
func ssColor(th *theme.Theme, it skillshare.Item) color.NRGBA {
	switch {
	case it.Notice:
		return th.Yellow
	case it.State == skillshare.Update:
		return th.Blue
	case it.WaitsForSync():
		return th.Yellow
	case it.State == skillshare.InSync:
		return th.Green
	}
	return th.Muted
}

// ssSection is a section's caps title and count, colored.
func (u *ui) ssSection(title string, n int, c color.NRGBA, right gl.Widget) ssW {
	th := u.th
	return func(gtx gl.Context) gl.Dimensions {
		h := gtx.Dp(36)
		gtx.Constraints = gl.Exact(image.Pt(gtx.Constraints.Max.X, h))
		gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Caption), th.Readable(c, th.Surface), fmt.Sprintf("%s · %d", strings.ToUpper(title), n))
			}),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				if right == nil {
					return gl.Dimensions{}
				}
				return right(gtx)
			}),
		)
		return gl.Dimensions{Size: gtx.Constraints.Max}
	}
}

// ssRow is one resource: its dot, name and short state, any buttons, and
// a chevron; a click previews it.
func (u *ui) ssRow(v skillshare.View, it skillshare.Item, showScope bool, buttons ...gl.Widget) ssW {
	th, s := u.th, &u.ss
	id := "row:" + it.Key
	s.acts[id] = func() { u.ssOpen(it, ssReadOf(v, it)) }
	return func(gtx gl.Context) gl.Dimensions {
		h := gtx.Dp(46)
		w := gtx.Constraints.Max.X
		gtx.Constraints = gl.Exact(image.Pt(w, h))
		c := s.click(id)
		// The buttons go over the row's clickable, so their clicks stay theirs.
		d := c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			if c.Hovered() {
				paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(-gtx.Dp(8), 0, w+gtx.Dp(4), h), gtx.Dp(8)).Op(gtx.Ops))
			}
			r := gtx.Dp(4)
			dot := image.Rect(0, h/2-r, 2*r, h/2+r)
			col := ssColor(th, it)
			if it.State == skillshare.Off || it.State == skillshare.New {
				paint.FillShape(gtx.Ops, col, clip.Stroke{Path: clip.Ellipse(dot).Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
			} else {
				paint.FillShape(gtx.Ops, col, clip.Ellipse(dot).Op(gtx.Ops))
			}
			tg := gtx
			tg.Constraints.Max.X = w - gtx.Dp(140)
			o := op.Offset(image.Pt(gtx.Dp(18), gtx.Dp(5))).Push(gtx.Ops)
			name := it.Label
			if it.Notice {
				name = it.Reason
			}
			u.ssLine(tg, medium(th.UIFont), th.Sp(theme.Body), th.Fg, name)
			o.Pop()
			o = op.Offset(image.Pt(gtx.Dp(18), gtx.Dp(25))).Push(gtx.Ops)
			state := skillshare.RowState(it, showScope)
			if it.Notice {
				state = it.Scope + " · hook notice"
			}
			u.ssLine(tg, th.UIFont, th.Sp(theme.Small), th.Muted, state)
			o.Pop()
			o = op.Offset(image.Pt(w-gtx.Dp(16), (h-gtx.Dp(14))/2)).Push(gtx.Ops)
			sidebar.Icon(gtx, "chevron-right", gtx.Dp(14), th.Muted)
			o.Pop()
			return gl.Dimensions{Size: image.Pt(w, h)}
		})
		if len(buttons) > 0 {
			rec := op.Record(gtx.Ops)
			bg := gtx
			bg.Constraints.Min = image.Point{}
			bd := ssButtons(bg, buttons...)
			call := rec.Stop()
			o := op.Offset(image.Pt(w-gtx.Dp(26)-bd.Size.X, (h-bd.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
		}
		return d
	}
}

// ssText is wrapped text as a list row.
func (u *ui) ssText(f font.Font, size unit.Sp, c color.NRGBA, s string) ssW {
	return func(gtx gl.Context) gl.Dimensions {
		d := para(gtx, u.th, f, size, c, s)
		d.Size.Y += gtx.Dp(4)
		return d
	}
}

func ssSpace(dp unit.Dp) ssW {
	return func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(0, gtx.Dp(dp))} }
}

// ssReading are the reads still out in the shown scopes, by name.
func ssReading(v skillshare.View, scope string) (reading []string, errs []string) {
	for _, sc := range []string{skillshare.Global, skillshare.Project} {
		r := v.Reads[sc]
		if r == nil || !skillshare.Shown(scope, sc) {
			continue
		}
		for _, n := range skillshare.ReadNames {
			if r.Pending[n] && !slices.Contains(reading, n) {
				reading = append(reading, n)
			}
			if e := r.Errors[n]; e != "" {
				errs = append(errs, sc+" "+n+": "+e)
			}
		}
	}
	return reading, errs
}

// ssOverviewRows is Overview: the answer and Sync, the counts per kind,
// a line per target, then only what needs the user.
func (u *ui) ssOverviewRows(v skillshare.View, busy bool) []ssW {
	th, s := u.th, &u.ss
	scope := s.scope
	known, targets := skillshare.Targets(v.Reads, scope, v.Items)
	var lag []skillshare.Target
	for _, t := range targets {
		if t.N > 0 {
			lag = append(lag, t)
		}
	}
	var waiting, updates, notices []skillshare.Item
	total := 0
	for _, it := range v.Items {
		if !skillshare.Shown(scope, it.Scope) {
			continue
		}
		switch {
		case it.Notice:
			notices = append(notices, it)
			continue
		case it.State == skillshare.Update:
			updates = append(updates, it)
		case it.WaitsForSync():
			waiting = append(waiting, it)
		}
		total++
	}
	reading, errs := ssReading(v, scope)
	if len(v.Reads) == 0 {
		reading = []string{"everything"}
	}
	ans := skillshare.Overview(busy && strings.HasPrefix(s.running(), "Sync"), reading, len(errs) > 0, len(waiting), lag, total, len(targets))
	rows := []ssW{u.ssAnswer(ans, !busy && (len(waiting) > 0 || len(lag) > 0), v)}
	rows = append(rows, ssSpace(12), u.ssCounts(v, scope), ssSpace(8))
	if known || len(targets) > 0 {
		rows = append(rows, u.ssTargets(targets, !known || len(reading) > 0 || len(errs) > 0)...)
	}
	for _, e := range errs {
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), e))
	}
	showScope := v.Project != ""
	if len(waiting) > 0 {
		rows = append(rows, ssSpace(8), u.ssSection("Waiting to sync", len(waiting), th.Yellow, nil))
		for _, it := range waiting {
			rows = append(rows, u.ssRow(v, it, showScope))
		}
	}
	if len(updates) > 0 {
		var all gl.Widget
		if len(updates) > 1 {
			ups := updates
			all = u.ssButton("ov-update-all", kit.Secondary.When(!busy), "Update all", func() { u.ssUpdate(ups, false, v.Project) })
		}
		rows = append(rows, ssSpace(8), u.ssSection("Update available", len(updates), th.Blue, all))
		for _, it := range updates {
			it := it
			rows = append(rows, u.ssRow(v, it, showScope, u.ssButton("upd:"+it.Key, kit.Secondary.When(!busy), "Update", func() {
				u.ssUpdate([]skillshare.Item{it}, false, v.Project)
			})))
		}
	}
	if len(notices) > 0 {
		rows = append(rows, ssSpace(8), u.ssSection("Hooks need a look", len(notices), th.Yellow, nil))
		for _, it := range notices {
			rows = append(rows, u.ssRow(v, it, showScope))
		}
	}
	return rows
}

// ssAnswer is Overview's headline with its mark, and Sync while
// something waits.
func (u *ui) ssAnswer(a skillshare.Answer, canSync bool, v skillshare.View) ssW {
	th := u.th
	icon, col := "check", th.Green
	switch a.Kind {
	case "waiting":
		icon, col = "circle-alert", th.Yellow
	case "failed":
		icon, col = "circle-alert", th.Red
	case "loading", "moving":
		icon, col = "loader", th.Muted
	}
	var sync gl.Widget
	if canSync {
		sync = u.ssButton("sync", kit.Primary, "Sync", func() { u.ssSync(v) })
	}
	return func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return sidebar.Icon(gtx, icon, gtx.Dp(20), th.Readable(col, th.Surface))
			}),
			gl.Rigid(ssGap(12)),
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, a.Headline)
					}),
					gl.Rigid(ssGap(2)),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, a.Sub)
					}),
				)
			}),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				if sync == nil {
					return gl.Dimensions{}
				}
				return sync(gtx)
			}),
		)
	}
}

// ssCounts is a box of five counts, Skills to Hooks; each opens Library
// on its kind. A count not known yet shows "…", never zero.
func (u *ui) ssCounts(v skillshare.View, scope string) ssW {
	th, s := u.th, &u.ss
	counts := skillshare.Counts(v.Items, scope)
	unsure := skillshare.Unsure(v.Reads, scope)
	waits := map[skillshare.Kind]bool{}
	for _, it := range v.Items {
		if skillshare.Shown(scope, it.Scope) && (it.WaitsForSync() || it.State == skillshare.Update) {
			waits[it.Kind] = true
		}
	}
	for _, k := range skillshare.Kinds {
		k := k
		s.acts["count:"+string(k)] = func() { s.library, s.kind, s.list.Position = true, k, gl.Position{} }
	}
	return func(gtx gl.Context) gl.Dimensions {
		w, h := gtx.Constraints.Max.X, gtx.Dp(64)
		box := image.Rect(0, 0, w, h)
		rr := gtx.Dp(10)
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.UniformRRect(box, rr).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box.Inset(1), rr-1).Op(gtx.Ops))
		cw := w / len(skillshare.Kinds)
		for i, k := range skillshare.Kinds {
			n := "…"
			if !unsure[k] && len(v.Reads) > 0 {
				n = strconv.Itoa(counts[k])
			}
			col := th.Fg
			if waits[k] {
				col = th.Readable(th.Yellow, th.Surface)
			}
			o := op.Offset(image.Pt(i*cw, 0)).Push(gtx.Ops)
			g := gtx
			g.Constraints = gl.Exact(image.Pt(cw, h))
			c := s.click("count:" + string(k))
			c.Layout(g, func(gtx gl.Context) gl.Dimensions {
				if c.Hovered() {
					paint.FillShape(gtx.Ops, th.Hover, clip.Rect{Min: image.Pt(1, 1), Max: image.Pt(cw-1, h-1)}.Op())
				}
				nc, nsz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), col, n)
				lc, lsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, k.Label())
				top := (h - nsz.Y - lsz.Y - gtx.Dp(2)) / 2
				o := op.Offset(image.Pt((cw-nsz.X)/2, top)).Push(gtx.Ops)
				nc.Add(gtx.Ops)
				o.Pop()
				o = op.Offset(image.Pt((cw-lsz.X)/2, top+nsz.Y+gtx.Dp(2))).Push(gtx.Ops)
				lc.Add(gtx.Ops)
				o.Pop()
				return gl.Dimensions{Size: image.Pt(cw, h)}
			})
			if i > 0 {
				paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(1, h)}.Op())
			}
			o.Pop()
		}
		return gl.Dimensions{Size: box.Max}
	}
}

// ssTargets is a line per target, waiting ones first, six at most; the
// rest are a count.
func (u *ui) ssTargets(ts []skillshare.Target, unknown bool) []ssW {
	th := u.th
	const most = 6
	shown := ts
	more := 0
	if len(ts) > most {
		shown, more = ts[:most-1], len(ts)-(most-1)
	}
	line := func(name, right string, dot, rc color.NRGBA, check bool) ssW {
		return func(gtx gl.Context) gl.Dimensions {
			w, h := gtx.Constraints.Max.X, gtx.Dp(30)
			x0 := gtx.Dp(14)
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(x0, 0), Max: image.Pt(x0+1, h/2)}.Op())
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(x0, h/2), Max: image.Pt(x0+gtx.Dp(22), h/2+1)}.Op())
			r := gtx.Dp(4)
			cx := x0 + gtx.Dp(30)
			paint.FillShape(gtx.Ops, dot, clip.Ellipse(image.Rect(cx-r, h/2-r, cx+r, h/2+r)).Op(gtx.Ops))
			call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, name)
			o := op.Offset(image.Pt(cx+gtx.Dp(12), (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			if check {
				o := op.Offset(image.Pt(w-gtx.Dp(14), (h-gtx.Dp(14))/2)).Push(gtx.Ops)
				sidebar.Icon(gtx, "check", gtx.Dp(14), rc)
				o.Pop()
			} else if right != "" {
				call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), rc, right)
				o := op.Offset(image.Pt(w-sz.X, (h-sz.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				o.Pop()
			}
			return gl.Dimensions{Size: image.Pt(w, h)}
		}
	}
	yellow, green := th.Readable(th.Yellow, th.Surface), th.Readable(th.Green, th.Surface)
	var rows []ssW
	for _, t := range shown {
		switch {
		case unknown:
			rows = append(rows, line(t.Name, "…", th.Muted, th.Muted, false))
		case t.N > 0:
			rows = append(rows, line(t.Name, fmt.Sprintf("%d to sync", t.N), th.Yellow, yellow, false))
		default:
			rows = append(rows, line(t.Name, "", th.Green, green, true))
		}
	}
	if more > 0 {
		rows = append(rows, line(fmt.Sprintf("+%d more", more), "", th.Muted, th.Muted, false))
	}
	return rows
}

// ssLibraryRows is Library: the kind tabs with counts, Sync and Install,
// then the kind's rows in sections.
func (u *ui) ssLibraryRows(v skillshare.View, busy bool) []ssW {
	th, s := u.th, &u.ss
	scope := s.scope
	counts := skillshare.Counts(v.Items, scope)
	unsure := skillshare.Unsure(v.Reads, scope)
	waits := slices.ContainsFunc(v.Items, func(it skillshare.Item) bool { return skillshare.Shown(scope, it.Scope) && it.WaitsForSync() })
	var opts [][2]string
	for _, k := range skillshare.Kinds {
		n := "…"
		if !unsure[k] && len(v.Reads) > 0 {
			n = strconv.Itoa(counts[k])
		}
		opts = append(opts, [2]string{string(k), k.Label() + " " + n})
	}
	syncKind := kit.Secondary
	if waits {
		syncKind = kit.Primary
	}
	project := v.Project
	head := func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min.X = 0
				return u.ssSegmented(gtx, "kind", opts, string(s.kind), func(k string) { s.kind = skillshare.Kind(k) })
			}),
			gl.Rigid(ssGap(8)),
			gl.Rigid(u.ssButton("lib-sync", syncKind.When(!busy), "Sync", func() { u.ssSync(v) })),
			gl.Rigid(ssGap(8)),
			gl.Rigid(u.ssButton("install", kit.Secondary.When(!busy), "Install…", func() {
				u.startInstall(project)
				u.modal.focus = false
			})),
		)
	}
	rows := []ssW{head, ssSpace(6)}
	_, errs := ssReading(v, scope)
	for _, e := range errs {
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), e))
	}
	g := skillshare.Group(v.Items, scope, s.kind)
	showScope := v.Project != "" && scope == "all"
	add := func(title string, list []skillshare.Item, c color.NRGBA, right gl.Widget, buttons func(skillshare.Item) []gl.Widget) {
		if len(list) == 0 {
			return
		}
		rows = append(rows, u.ssSection(title, len(list), c, right))
		for _, it := range list {
			var bs []gl.Widget
			if buttons != nil {
				bs = buttons(it)
			}
			rows = append(rows, u.ssRow(v, it, showScope, bs...))
		}
		rows = append(rows, ssSpace(6))
	}
	add("Sync status unknown", g.Unknown, th.Muted, nil, nil)
	add("Need a look", g.Look, th.Yellow, nil, nil)
	add("Needs sync", g.Sync, th.Yellow, nil, nil)
	var all gl.Widget
	if len(g.Update) > 1 && (s.kind == skillshare.Skills || s.kind == skillshare.Agents) {
		ups := g.Update
		all = u.ssButton("update-all", kit.Secondary.When(!busy), "Update all", func() { u.ssUpdate(ups, false, project) })
	}
	add("Update available", g.Update, th.Blue, all, func(it skillshare.Item) []gl.Widget {
		return []gl.Widget{u.ssButton("upd:"+it.Key, kit.Secondary.When(!busy), "Update", func() {
			u.ssUpdate([]skillshare.Item{it}, false, project)
		})}
	})
	add("In sync", g.InSync, th.Green, nil, nil)
	add("Off", g.Off, th.Muted, nil, nil)
	if len(g.Unknown)+len(g.Look)+len(g.Sync)+len(g.Update)+len(g.InSync)+len(g.Off) == 0 {
		msg := "No " + strings.ToLower(s.kind.Label()) + " here."
		if len(v.Reads) == 0 || unsure[s.kind] {
			msg = "Reading skillshare…"
		}
		rows = append(rows, ssSpace(16), u.ssText(th.UIFont, th.Sp(theme.Body), th.Muted, msg))
	}
	return rows
}

// ssPreviewRows is a row's preview: what it is, its actions, its targets,
// and its document or settings.
func (u *ui) ssPreviewRows(st *model.State, v skillshare.View) []ssW {
	th, s := u.th, &u.ss
	i := slices.IndexFunc(v.Items, func(it skillshare.Item) bool { return it.Key == s.sel })
	if i < 0 {
		s.sel = ""
		return nil
	}
	it := v.Items[i]
	busy := s.running() != ""
	project := v.Project
	back := u.ssButton("back", kit.Ghost, "‹ Back", func() { s.sel = "" })
	rows := []ssW{func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Rigid(back), gl.Rigid(ssGap(8)),
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, cmpLabel(it))
					}),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, it.Scope+" · "+it.Kind.Label()+" · "+skillshare.RowState(it, false))
					}),
				)
			}),
		)
	}, ssSpace(10)}

	var acts []gl.Widget
	if it.State == skillshare.Update {
		acts = append(acts, u.ssButton("p-update", kit.Primary.When(!busy), "Update", func() { u.ssUpdate([]skillshare.Item{it}, false, project) }))
	}
	if it.WaitsForSync() {
		acts = append(acts, u.ssButton("p-sync", kit.Primary.When(!busy), "Sync", func() { u.ssSync(v) }))
	}
	if it.Kind == skillshare.Skills || it.Kind == skillshare.Agents || it.Kind == skillshare.Hooks && it.Declared && it.Root == "" && !it.Notice {
		verb, label := "disable", "Disable"
		if it.Disabled {
			verb, label = "enable", "Enable"
		}
		acts = append(acts, u.ssButton("p-toggle", kit.Secondary.When(!busy), label, func() {
			u.ssCall(label+"…", skillshare.Call{Op: skillshare.OpToggle, Item: it, Action: verb, Dir: project})
		}))
	}
	if it.Kind == skillshare.Skills || it.Kind == skillshare.Agents || it.Kind == skillshare.Hooks && it.Declared && it.Root == "" && !it.Notice {
		if s.confirm {
			acts = append(acts,
				u.ssButton("p-uninstall-yes", kit.Danger.When(!busy), "Uninstall "+it.Label, func() {
					s.confirm, s.sel = false, ""
					u.ssCall("Uninstalling…", skillshare.Call{Op: skillshare.OpUninstall, Item: it, Dir: project})
				}),
				u.ssButton("p-uninstall-no", kit.Ghost, "Cancel", func() { s.confirm = false }))
		} else {
			acts = append(acts, u.ssButton("p-uninstall", kit.Secondary.When(!busy), "Uninstall…", func() { s.confirm = true }))
		}
		if p := u.ssAgent(st); p != nil && u.hostOf(p.ID) == s.to.host && s.shown.Path != "" {
			path := s.shown.Path
			acts = append(acts, u.ssButton("p-ask", kit.Secondary, "Ask "+agentName(p)+" to audit", func() { s.ask = path }))
		}
	}
	if len(acts) > 0 {
		rows = append(rows, func(gtx gl.Context) gl.Dimensions { return ssButtons(gtx, acts...) }, ssSpace(6))
	}
	if s.confirm {
		text := "Uninstall moves it to the skillshare trash; skillshare trash restores it."
		if it.Kind == skillshare.Hooks {
			text = "Uninstall removes this hook from the source. Sync applies it to targets."
		}
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Muted, text))
	}

	// Who gets it, waiting targets first.
	if lines := ssPreviewTargets(it, ssReadOf(v, it)); len(lines) > 0 {
		rows = append(rows, ssSpace(6), u.ssSection("Targets", len(lines), th.Muted, nil))
		for _, l := range lines {
			l := l
			rows = append(rows, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						r := gtx.Dp(4)
						col := th.Green
						if l.warn {
							col = th.Yellow
						}
						paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, gtx.Dp(11)-r, 2*r, gtx.Dp(11)+r)).Op(gtx.Ops))
						return gl.Dimensions{Size: image.Pt(2*r, gtx.Dp(22))}
					}),
					gl.Rigid(ssGap(10)),
					gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
						d := u.ssLine(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, l.name)
						d.Size.X = gtx.Constraints.Max.X
						return d
					}),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, l.text)
					}),
				)
			})
		}
	}

	p := s.shown
	rows = append(rows, ssSpace(10))
	if p.Path != "" {
		rows = append(rows, u.ssText(th.MonoFont, th.Sp(theme.Caption), th.Muted, shortHome(p.Path)))
	}
	if p.Err != "" {
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), p.Err))
	}
	if len(p.Files) > 1 {
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Muted, "Files: "+strings.Join(p.Files, ", ")))
	}
	kvs := func(list []skillshare.KV) {
		for _, kv := range list {
			kv := kv
			rows = append(rows, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						gtx.Constraints = gl.Exact(image.Pt(gtx.Dp(120), gtx.Dp(22)))
						u.ssLine(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, kv.Key)
						return gl.Dimensions{Size: gtx.Constraints.Max}
					}),
					gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
						d := para(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Fg, kv.Value)
						d.Size.Y = max(d.Size.Y+gtx.Dp(4), gtx.Dp(22))
						return d
					}),
				)
			})
		}
	}
	kvs(p.Meta)
	kvs(p.MCP)
	switch it.Kind {
	case skillshare.Hooks:
		if it.Reason != "" && !it.Notice {
			rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Body), th.Fg, it.Reason))
		}
		for _, l := range it.Lines {
			rows = append(rows, u.ssText(th.MonoFont, th.Sp(theme.Small), th.Fg, l))
		}
	case skillshare.Plugins:
		kvs([]skillshare.KV{{Key: "source", Value: it.Src}, {Key: "version", Value: it.Version}})
	}
	if len(s.doc.Blocks()) > 0 {
		rows = append(rows, ssSpace(8), func(gtx gl.Context) gl.Dimensions {
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
			o := op.Offset(image.Pt(0, gtx.Dp(12))).Push(gtx.Ops)
			s.doc.Size = th.Sp(theme.Body)
			d := s.doc.Layout(gtx, th)
			o.Pop()
			d.Size.Y += gtx.Dp(12)
			return d
		})
	}
	return rows
}

func cmpLabel(it skillshare.Item) string {
	if it.Notice {
		return "Hook notice"
	}
	return it.Label
}

type ssTargetLine struct {
	name, text string
	warn       bool
}

// ssPreviewTargets is who gets it and what each still needs: for a skill
// or an agent every target its scope sends that kind to, else the ones
// its own read names.
func ssPreviewTargets(it skillshare.Item, r *skillshare.Read) []ssTargetLine {
	var out []ssTargetLine
	switch it.Kind {
	case skillshare.Skills, skillshare.Agents:
		var names []string
		if r != nil && r.Status != nil {
			if it.Kind == skillshare.Agents {
				for _, t := range r.Status.Agents.Targets {
					names = append(names, t.Name)
				}
			} else {
				for _, t := range r.Status.Targets {
					if t.SkillsEnabled == nil || *t.SkillsEnabled {
						names = append(names, t.Name)
					}
				}
			}
		}
		for _, t := range it.Targets {
			if !slices.Contains(names, t) {
				names = append(names, t)
			}
		}
		for _, n := range names {
			l := ssTargetLine{name: n, text: "in sync"}
			switch {
			case it.State == skillshare.Off:
				l.text = "disabled"
			case slices.Contains(it.Targets, n):
				l.text, l.warn = "needs sync", true
			}
			out = append(out, l)
		}
	case skillshare.MCP, skillshare.Hooks, skillshare.Plugins:
		waits := map[string]string{}
		for _, p := range strings.Split(strings.TrimPrefix(it.Reason, "pending in "), ", ") {
			if t, a, ok := strings.Cut(p, ": "); ok {
				waits[t] = a
			} else if p != "" && it.Kind == skillshare.Plugins {
				waits[p] = "pending"
			}
		}
		for _, t := range it.Targets {
			l := ssTargetLine{name: t, text: "in sync"}
			if a, ok := waits[t]; ok {
				l.text, l.warn = a, true
			} else if it.Kind == skillshare.Hooks && it.State == skillshare.Changed {
				l.text, l.warn = "waits for a sync", true
			}
			out = append(out, l)
		}
	}
	if it.State == skillshare.Unknown {
		for i := range out {
			out[i].text, out[i].warn = "sync status unknown", false
		}
	}
	slices.SortStableFunc(out, func(a, b ssTargetLine) int {
		if a.warn != b.warn {
			if a.warn {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	return out
}

// ssInstallRows is the install steps: the source, the skills to pick,
// the audit and the install. A blocked audit shows its findings before
// anything is written; Install anyway shows only then.
func (u *ui) ssInstallRows(gtx gl.Context, v skillshare.View) []ssW {
	th, s := u.th, &u.ss
	s.mu.Lock()
	in := s.inst
	step, work, errText, plan, audit, done := in.step, in.work, in.err, in.plan, in.audit, slices.Clone(in.done)
	picked := map[string]bool{}
	for k, b := range in.picked {
		picked[k] = b
	}
	s.mu.Unlock()
	project := v.Project
	inv := u.invalidateFn()
	if u.modal.focus || step == 0 && !gtx.Focused(&in.url) && !work && in.url.Len() == 0 {
		u.modal.focus = false
		gtx.Execute(key.FocusCmd{Tag: &in.url})
	}
	back := u.ssButton("i-back", kit.Ghost, "‹ Back", func() {
		s.dismissInstall()
	})
	steps := []string{"Source", "Choose", "Audit", "Install"}
	rows := []ssW{func(gtx gl.Context) gl.Dimensions {
		var kids []gl.FlexChild
		kids = append(kids, gl.Rigid(back), gl.Rigid(ssGap(8)), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.ssLine(gtx, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Install skills")
		}), gl.Flexed(1, ssFill))
		for i, name := range steps {
			col := th.Muted
			switch {
			case i == step && (errText != "" || audit != nil):
				col = th.Readable(th.Red, th.Surface)
			case i == min(step, 3):
				col = th.Fg
			case i < step:
				col = th.Readable(th.Green, th.Surface)
			}
			if i > 0 {
				kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "  ›  ")
				}))
			}
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return u.ssLine(gtx, medium(th.UIFont), th.Sp(theme.Small), col, name)
			}))
		}
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
	}, ssSpace(14)}

	switch {
	case step == 0:
		next := u.ssButton("i-next", kit.Primary.When(!work && strings.TrimSpace(in.url.Text()) != ""), "Next", func() {
			s.mu.Lock()
			u.ssDiscover(project)
			s.mu.Unlock()
		})
		rows = append(rows, u.ssText(medium(th.UIFont), th.Sp(theme.Small), th.Muted, "A repository: owner/repo, a git URL, or a folder of one skill in it"),
			func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
					gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
						if work {
							gtx = gtx.Disabled()
						}
						return u.field(gtx, &in.url, th.UIFont, gtx.Dp(36), "owner/repo")
					}),
					gl.Rigid(ssGap(8)), gl.Rigid(next))
			}, ssSpace(10))
		if project != "" {
			rows = append(rows, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "Install into  ")
					}),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						if work {
							gtx = gtx.Disabled()
						}
						return u.ssSegmented(gtx, "i-scope", [][2]string{{skillshare.Project, "Project"}, {skillshare.Global, "Global"}}, in.scope, func(k string) {
							s.mu.Lock()
							defer s.mu.Unlock()
							if !in.work && in.step == 0 {
								in.scope = k
							}
						})
					}))
			})
		} else {
			rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Muted, "Installs into the global source."))
		}
		if work {
			rows = append(rows, ssSpace(8), u.ssText(th.UIFont, th.Sp(theme.Body), th.Muted, "Reading the repository…"))
		}
	case step == 1:
		n := 0
		for _, c := range plan.Choices {
			if picked[c.Name] {
				n++
			}
		}
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Muted, fmt.Sprintf("%d skills in %s. Pick the ones to install.", len(plan.Choices), strings.TrimSpace(in.url.Text()))))
		for _, c := range plan.Choices {
			c := c
			id := "pick:" + c.Name
			s.acts[id] = func() {
				s.mu.Lock()
				in.picked[c.Name] = !in.picked[c.Name]
				s.mu.Unlock()
			}
			on := picked[c.Name]
			rows = append(rows, func(gtx gl.Context) gl.Dimensions {
				h := gtx.Dp(40)
				gtx.Constraints = gl.Exact(image.Pt(gtx.Constraints.Max.X, h))
				return s.click(id).Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					b := gtx.Dp(16)
					box := image.Rect(0, (h-b)/2, b, (h+b)/2)
					if on {
						paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(box, gtx.Dp(4)).Op(gtx.Ops))
						o := op.Offset(box.Min.Add(image.Pt(gtx.Dp(2), gtx.Dp(2)))).Push(gtx.Ops)
						sidebar.Icon(gtx, "check", gtx.Dp(12), th.OnPrimary)
						o.Pop()
					} else {
						paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: clip.UniformRRect(box, gtx.Dp(4)).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
					}
					o := op.Offset(image.Pt(b+gtx.Dp(12), gtx.Dp(3))).Push(gtx.Ops)
					u.ssLine(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, c.Name)
					o.Pop()
					o = op.Offset(image.Pt(b+gtx.Dp(12), gtx.Dp(21))).Push(gtx.Ops)
					u.ssLine(gtx, th.MonoFont, th.Sp(theme.Caption), th.Muted, c.Path)
					o.Pop()
					return gl.Dimensions{Size: gtx.Constraints.Max}
				})
			})
		}
		label := "Install"
		if n > 0 {
			label = fmt.Sprintf("Install %d", n)
		}
		rows = append(rows, ssSpace(8), func(gtx gl.Context) gl.Dimensions {
			return ssButtons(gtx, u.ssButton("i-go", kit.Primary.When(n > 0), label, func() {
				s.mu.Lock()
				u.ssAuditLocked(in, project, inv)
				s.mu.Unlock()
			}))
		})
	case audit != nil:
		red := th.Readable(th.Red, th.Surface)
		what := fmt.Sprintf("%d findings in %s", len(audit.Findings), strings.Join(audit.Blocked, ", "))
		if len(audit.Findings) > 0 {
			what += ", the worst " + audit.Findings[0].Severity // sorted worst first
		}
		rows = append(rows, u.ssText(semibold(th.UIFont), th.Sp(theme.Body), red, "Audit stopped the install"),
			u.ssText(th.UIFont, th.Sp(theme.Small), th.Fg, what+". Nothing was written yet."),
			ssSpace(6))
		for i, f := range audit.Findings {
			if i == 12 {
				rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Small), th.Muted, fmt.Sprintf("and %d more", len(audit.Findings)-i)))
				break
			}
			where := f.File
			if f.Line > 0 {
				where += ":" + strconv.Itoa(f.Line)
			}
			rows = append(rows, u.ssText(semibold(th.MonoFont), th.Sp(theme.Small), red, f.Severity+" "+f.Pattern+"  "+f.Skill+"/"+where),
				u.ssText(th.MonoFont, th.Sp(theme.Small), th.Muted, ssCut(f.Snippet, 120)), ssSpace(4))
		}
		rows = append(rows, ssSpace(8), func(gtx gl.Context) gl.Dimensions {
			return ssButtons(gtx,
				u.ssButton("i-cancel", kit.Secondary, "Cancel", func() {
					s.dismissInstall()
				}),
				u.ssButton("i-anyway", kit.Danger, "Install anyway", func() { u.ssInstallNow(in, project, true, inv) }))
		})
	case step == 4:
		name := strings.Join(done, ", ")
		rows = append(rows, u.ssText(semibold(th.UIFont), th.Sp(theme.Body), th.Readable(th.Green, th.Surface), name+" installed"),
			u.ssText(th.UIFont, th.Sp(theme.Small), th.Fg, "Sync to give it to your agents."), ssSpace(8),
			func(gtx gl.Context) gl.Dimensions {
				return ssButtons(gtx,
					u.ssButton("i-sync", kit.Primary.When(s.running() == ""), "Sync", func() { s.dismissInstall(); u.ssSync(s.cur().store.View()) }),
					u.ssButton("i-again", kit.Secondary, "Install another", func() { u.startInstall(project) }))
			})
	case work:
		what := "Auditing…"
		if step == 3 {
			what = "Installing…"
		}
		rows = append(rows, u.ssText(th.UIFont, th.Sp(theme.Body), th.Muted, what))
	}
	if errText != "" {
		rows = append(rows, ssSpace(8), u.ssText(th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), errText))
	}
	return rows
}

func ssCut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ssStatus is the line under the list: what runs now, or how the last
// command ended.
func (u *ui) ssStatus(gtx gl.Context, busy string, note skillshare.Outcome) {
	th := u.th
	icon, col, text := "check", th.Readable(th.Green, th.Surface), note.Text
	switch {
	case busy != "":
		icon, col, text = "loader", th.Muted, busy
	case note.Err:
		icon, col = "circle-alert", th.Readable(th.Red, th.Surface)
	}
	gl.Flex{Alignment: gl.Middle}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.Icon(gtx, icon, gtx.Dp(14), col) }),
		gl.Rigid(ssGap(8)),
		gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return u.ssLine(gtx, th.UIFont, th.Sp(theme.Small), col, text) }),
	)
}
