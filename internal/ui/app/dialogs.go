package app

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
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

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type modalKind int

const (
	modalNone modalKind = iota
	modalDelete
	modalSettings
	modalAddProject
)

// modal is the window-level dialog: aide's DeleteWorkspaceModal, a settings
// sheet listing the keybindings, and the add-project folder prompt. While
// one shows, panes give up key focus.
type modal struct {
	kind         modalKind
	ws           string // the workspace a delete dialog is about
	removeBranch bool
	focus        bool // move key focus into the dialog this frame

	check, cancel, ok widget.Clickable
	path              widget.Editor
	pathErr           string
	matches           []string // directories completing the path field

	backdrop, body int         // tags: the click-outside catcher, the dialog's own area
	list           widget.List // the settings sheet's shortcuts
}

func (m *modal) open(kind modalKind, ws string) {
	m.kind, m.ws, m.removeBranch, m.focus, m.pathErr, m.matches = kind, ws, false, true, "", nil
	if kind == modalAddProject {
		m.path.SingleLine, m.path.Submit = true, true
		m.path.SetText("~/")
		m.path.SetCaret(2, 2)
		m.matches = dirMatches("~/")
	}
}

func (m *modal) close() { m.kind = modalNone }

// layoutModal handles the dialog's input and draws it over the window.
func (u *ui) layoutModal(gtx gl.Context, st *model.State) {
	m := &u.modal
	ws := findWorkspace(st, m.ws)
	if m.kind == modalDelete && ws == nil {
		m.close() // deleted elsewhere
	}
	if m.kind == modalNone {
		return
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &m.body, Kinds: pointer.Press}); !ok {
			break
		}
	}
	confirm := false
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: &m.backdrop, Kinds: pointer.Press},
			key.Filter{Focus: &m.backdrop, Name: key.NameEscape},
			key.Filter{Focus: &m.backdrop, Name: key.NameReturn},
			key.Filter{Focus: &m.backdrop, Name: key.NameEnter},
			key.Filter{Focus: &m.path, Name: key.NameEscape},
			key.Filter{Focus: &m.path, Name: key.NameTab},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			// The dialog's own area sits on top of the backdrop and takes
			// presses inside it, so only presses outside land here.
			if e.Kind == pointer.Press {
				m.close()
			}
		case key.Event:
			if e.State != key.Press {
				break
			}
			switch e.Name {
			case key.NameEscape:
				m.close()
			case key.NameTab:
				if done, matches := completePath(m.path.Text()); done != m.path.Text() || len(matches) > 0 {
					m.path.SetText(done)
					n := utf8.RuneCountInString(done)
					m.path.SetCaret(n, n)
					m.matches = matches
				}
			default:
				confirm = true
			}
		}
	}
	if m.kind == modalNone {
		return
	}
	for {
		ev, ok := m.path.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			confirm = true
		case widget.ChangeEvent:
			m.pathErr = ""
			m.matches = dirMatches(m.path.Text())
		}
	}
	for m.check.Clicked(gtx) {
		m.removeBranch = !m.removeBranch
	}
	for m.cancel.Clicked(gtx) {
		m.close()
	}
	for m.ok.Clicked(gtx) {
		confirm = true
	}
	if confirm {
		u.confirmModal()
	}
	if m.kind == modalNone { // closed by Escape, Cancel, or a confirm above
		return
	}

	// Backdrop: catches clicks outside the dialog and holds key focus when
	// the dialog has no text field.
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0xc8}, clip.Rect{Max: size}.Op())
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.backdrop)
	bg.Pop()
	if m.focus {
		m.focus = false
		tag := event.Tag(&m.backdrop)
		if m.kind == modalAddProject {
			tag = &m.path
		}
		gtx.Execute(key.FocusCmd{Tag: tag})
	}

	th := u.th
	width := min(gtx.Dp(448), size.X-gtx.Dp(32))
	if m.kind == modalSettings {
		width = min(gtx.Dp(560), size.X-gtx.Dp(32))
	}
	var content gl.Widget
	switch m.kind {
	case modalDelete:
		content = func(gtx gl.Context) gl.Dimensions { return u.deleteBody(gtx, st, ws) }
	case modalSettings:
		content = u.settingsBody
	case modalAddProject:
		content = u.addProjectBody
	}
	pad := gtx.Dp(24)
	rec := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints = gl.Constraints{Max: image.Pt(width-2*pad, size.Y-2*pad)}
	d := content(cg)
	call := rec.Stop()
	box := image.Pt(width, d.Size.Y+2*pad)
	at := size.Sub(box).Div(2)
	defer op.Offset(at).Push(gtx.Ops).Pop()

	// rounded-xl border border-border bg-surface shadow-[0_20px_60px_rgba(0,0,0,0.4)]
	r := gtx.Dp(16)
	rect := image.Rectangle{Max: box}
	for i, a := range []uint8{0x20, 0x20, 0x20} {
		g := gtx.Dp(unit.Dp(8 * (i + 1)))
		paint.FillShape(gtx.Ops, color.NRGBA{A: a}, clip.UniformRRect(rect.Add(image.Pt(0, gtx.Dp(12))).Inset(-g), r+g).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.07), clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
	body := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &m.body)
	body.Pop()
	o := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}

func (u *ui) confirmModal() {
	m := &u.modal
	switch m.kind {
	case modalDelete:
		u.send(proto.DeleteWorkspace{WorkspaceID: m.ws, RemoveBranch: m.removeBranch})
		m.close()
	case modalSettings:
		m.close()
	case modalAddProject:
		p, err := resolveDir(m.path.Text())
		if err != nil {
			m.pathErr = err.Error()
			return
		}
		u.send(proto.AddProject{Path: p})
		m.close()
	}
}

// deleteBody is DeleteWorkspaceModal. pitwall always removes a linked
// worktree with its workspace, so where aide offers to delete the folder,
// this offers to delete the branch too.
func (u *ui) deleteBody(gtx gl.Context, st *model.State, ws *model.Workspace) gl.Dimensions {
	th := u.th
	worktree := ws.WorktreeRoot != "" // the daemon removes only worktrees it made
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Delete \""+tabTitle(*ws)+"\"?")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 14, th.Muted, "Stops its terminals and agents and removes it from pitwall.")
		}),
	}
	if worktree {
		kids = append(kids,
			gl.Rigid(gl.Spacer{Height: 12}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.UIFont, 14, th.Fg, "Its worktree folder is removed:")
			}),
			gl.Rigid(gl.Spacer{Height: 2}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.MonoFont, 12, th.Muted, ws.Path)
			}),
			gl.Rigid(gl.Spacer{Height: 2}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.UIFont, 12, th.Muted, "Git refuses if it has uncommitted files.")
			}),
		)
		if ws.Branch != "" {
			kids = append(kids,
				gl.Rigid(gl.Spacer{Height: 12}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return u.checkbox(gtx, &u.modal.check, u.modal.removeBranch, "Also delete the branch "+ws.Branch,
						"git branch -d refuses if it is not merged.")
				}),
			)
		}
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Delete", th.Red, theme.Hex("#ffffff"))
		}),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

type shortcut struct {
	keys []string // keycaps, one per chord
	what string
}

// shortcuts are the settings sheet's rows for b: every bound action, the
// 1-9 series folded into one row, tab-mode keys after the prefix.
func shortcuts(b *config.Bindings) []shortcut {
	var out []shortcut
	if hk := b.HoldKey(); hk != "" {
		out = append(out, shortcut{[]string{"Hold " + string(hk)}, "Show every tab; the tab keys walk them all"})
	}
	prefix := firstChord(b.Global["tab_prefix"])
	for _, a := range config.Actions() {
		cs := b.Global[a.Name]
		if a.Tab {
			cs = b.Tab[a.Name]
			if prefix == "" {
				continue
			}
		}
		series, n := a.Name, ""
		if i := strings.LastIndex(a.Name, "_"); i > 0 {
			series, n = a.Name[:i], a.Name[i+1:]
		}
		isSeries := len(n) == 1 && n[0] >= '1' && n[0] <= '9'

		what := a.Doc
		if isSeries {
			if n != "1" {
				continue
			}
			last := b.Global[series+"_9"]
			if a.Tab {
				last = b.Tab[series+"_9"]
			}
			if len(cs) == 0 || len(last) == 0 {
				continue
			}
			what = strings.TrimSuffix(a.Doc, " 1") + " 1-9"
			cs = []config.Chord{cs[0], last[0]}
		}
		if len(cs) == 0 {
			continue
		}
		var keys []string
		for i, c := range cs[:min(2, len(cs))] {
			k := c.String()
			if isSeries && i == 1 {
				k = "… " + k
			}
			keys = append(keys, k)
		}
		if a.Tab {
			keys = append([]string{prefix + ", then"}, keys...)
			what = "Tab mode: " + strings.ToLower(what[:1]) + what[1:]
		}
		if before, _, ok := strings.Cut(what, ". "); ok {
			what = before
		}
		out = append(out, shortcut{keys, what})
	}
	return append(out, shortcut{[]string{"Ctrl / Shift", "Click"}, "Pick tabs to group"})
}

func (u *ui) settingsBody(gtx gl.Context) gl.Dimensions {
	th, m := u.th, &u.modal
	b := u.nav.bind()
	rows := shortcuts(b)
	path := u.cfg.Path
	if path == "" {
		path = config.Path()
	}
	themeName := u.cfg.ThemeName
	if themeName == "" {
		themeName = "aide-dark"
	}
	info := func(k, v string, f font.Font) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			h := gtx.Dp(24)
			call, sz := textCall(gtx, th, th.UIFont, 13, th.Muted, k)
			o := op.Offset(image.Pt(0, (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			vg := gtx
			vg.Constraints.Max.X -= gtx.Dp(72)
			call, sz = textCall(vg, th, f, 13, th.Fg, v)
			o = op.Offset(image.Pt(gtx.Dp(72), (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
		})
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Settings")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		info("Config", sidebar.ShortPath(path), th.MonoFont),
		info("Keys", b.Preset+" preset", th.UIFont),
		info("Theme", themeName, th.UIFont),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, medium(th.UIFont), 11, th.Muted, "Keyboard shortcuts")
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
			m.list.Axis = gl.Vertical
			return m.list.Layout(gtx, len(rows), func(gtx gl.Context, i int) gl.Dimensions {
				kb := rows[i]
				h := gtx.Dp(32)
				w := gtx.Constraints.Max.X
				var caps []op.CallOp
				var sizes []image.Point
				capsW := 0
				for _, k := range kb.keys {
					kc, ks := keycap(gtx, th, k)
					caps, sizes = append(caps, kc), append(sizes, ks)
					capsW += ks.X + gtx.Dp(4)
				}
				tg := gtx
				tg.Constraints.Max.X = max(0, w-capsW-gtx.Dp(8))
				call, sz := textCall(tg, th, th.UIFont, 13, th.Fg, kb.what)
				o := op.Offset(image.Pt(0, (h-sz.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				o.Pop()
				x := w
				for i := len(caps) - 1; i >= 0; i-- {
					x -= sizes[i].X
					o := op.Offset(image.Pt(x, (h-sizes[i].Y)/2)).Push(gtx.Ops)
					caps[i].Add(gtx.Ops)
					o.Pop()
					x -= gtx.Dp(4)
				}
				return gl.Dimensions{Size: image.Pt(w, h)}
			})
		}),
		gl.Rigid(gl.Spacer{Height: 20}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "", "Done", th.Primary, th.OnPrimary)
		}),
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

func (u *ui) addProjectBody(gtx gl.Context) gl.Dimensions {
	th, m := u.th, &u.modal
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Open folder as group")
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 14, th.Muted, "Makes a group for the folder. A git repository also gets worktree tabs.")
		}),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			// field: h-9, field-background, field-border, focus ring primary
			w, h := gtx.Constraints.Max.X, gtx.Dp(36)
			rect := image.Rect(0, 0, w, h)
			r := gtx.Dp(8)
			border := theme.Mix(th.SurfaceSecondary, th.Fg, 0.07)
			if gtx.Focused(&m.path) {
				border = theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
			}
			paint.FillShape(gtx.Ops, border, clip.UniformRRect(rect, r).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
			eg := gtx
			eg.Constraints = gl.Exact(image.Pt(w-gtx.Dp(24), h))
			o := op.Offset(image.Pt(gtx.Dp(12), 0)).Push(gtx.Ops)
			gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
				return m.path.Layout(gtx, th.Shaper, th.MonoFont, 13, colorCall(gtx, th.Fg), colorCall(gtx, theme.Mix(th.SurfaceSecondary, th.Primary, 0.35)))
			})
			o.Pop()
			return gl.Dimensions{Size: rect.Size()}
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
	}
	if m.pathErr != "" {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 12, th.Red, m.pathErr)
		}))
	} else {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 12, th.Muted, "Tab completes a folder name. Enter adds it.")
		}))
	}
	if len(m.matches) > 0 {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout))
		for i, name := range m.matches {
			if i == 6 {
				kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return para(gtx, th, th.UIFont, 12, th.Muted, "and more")
				}))
				break
			}
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.MonoFont, 12, theme.Mix(th.Surface, th.Fg, 0.8), name+"/")
			}))
		}
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 20}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Open", th.Primary, th.OnPrimary)
		}),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// buttons is a dialog footer: an optional secondary button and the primary
// one, right-aligned.
func (u *ui) buttons(gtx gl.Context, cancel, ok string, okBg, okFg color.NRGBA) gl.Dimensions {
	th, m := u.th, &u.modal
	h := gtx.Dp(36)
	w := gtx.Constraints.Max.X
	x := w
	draw := func(c *widget.Clickable, text string, bg, fg color.NRGBA) {
		call, sz := textCall(gtx, th, medium(th.UIFont), 14, fg, text)
		bw := sz.X + 2*gtx.Dp(16)
		x -= bw
		o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(bw, h))
		c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			b := bg
			if c.Hovered() {
				b = theme.Mix(bg, th.Fg, 0.08)
			}
			rr := clip.UniformRRect(image.Rect(0, 0, bw, h), h/2)
			paint.FillShape(gtx.Ops, b, rr.Op(gtx.Ops))
			pointer.CursorPointer.Add(gtx.Ops)
			t := op.Offset(image.Pt((bw-sz.X)/2, (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			t.Pop()
			return gl.Dimensions{Size: image.Pt(bw, h)}
		})
		o.Pop()
		x -= gtx.Dp(8)
	}
	draw(&m.ok, ok, okBg, okFg)
	if cancel != "" {
		draw(&m.cancel, cancel, th.SurfaceSecondary, th.Fg)
	}
	return gl.Dimensions{Size: image.Pt(w, h)}
}

// checkbox draws a 16px box and a label with a muted second line.
func (u *ui) checkbox(gtx gl.Context, c *widget.Clickable, on bool, text, note string) gl.Dimensions {
	th := u.th
	return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		box := gtx.Dp(16)
		rect := image.Rect(0, gtx.Dp(2), box, gtx.Dp(2)+box)
		r := gtx.Dp(4)
		if on {
			paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(rect, r).Op(gtx.Ops))
			var p clip.Path
			p.Begin(gtx.Ops)
			s := float32(box) / 16
			o := f32.Pt(0, float32(gtx.Dp(2)))
			p.MoveTo(o.Add(f32.Pt(4*s, 8.5*s)))
			p.LineTo(o.Add(f32.Pt(7*s, 11.5*s)))
			p.LineTo(o.Add(f32.Pt(12*s, 5*s)))
			paint.FillShape(gtx.Ops, th.OnPrimary, clip.Stroke{Path: p.End(), Width: 2 * s}.Op())
		} else {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.25), clip.UniformRRect(rect, r).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		}
		x := box + gtx.Dp(8)
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(gtx.Constraints.Max.X-x, gtx.Constraints.Max.Y)}
		o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		d1 := para(g, th, th.UIFont, 14, th.Fg, text)
		o.Pop()
		o = op.Offset(image.Pt(x, d1.Size.Y+gtx.Dp(2))).Push(gtx.Ops)
		d2 := para(g, th, th.UIFont, 12, th.Muted, note)
		o.Pop()
		size := image.Pt(gtx.Constraints.Max.X, d1.Size.Y+gtx.Dp(2)+d2.Size.Y)
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
}

// keycap is aide's .keycap: 20px tall, surface-3 to surface-1 gradient, a
// border-token ring, 12px body text.
func keycap(gtx gl.Context, th *theme.Theme, s string) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	call, ts := textCall(gtx, th, th.UIFont, 12, theme.Mix(th.Muted, th.Fg, 0.54), s)
	h := gtx.Dp(20)
	sz := image.Pt(max(ts.X+2*gtx.Dp(6), h), h)
	r := gtx.Dp(4)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: sz}, r).Op(gtx.Ops))
	in := clip.UniformRRect(image.Rect(1, 1, sz.X-1, sz.Y-1), r-1).Push(gtx.Ops)
	paint.LinearGradientOp{
		Stop1: f32.Pt(0, 0), Color1: th.SurfaceElevated,
		Stop2: f32.Pt(0, float32(sz.Y)), Color2: th.Surface,
	}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	in.Pop()
	o := op.Offset(sz.Sub(ts).Div(2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return m.Stop(), sz
}

// para draws wrapping text at the constraints' width.
func para(gtx gl.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, s string) gl.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{}.Layout(gtx, th.Shaper, f, size, s, colorCall(gtx, c))
}

func medium(f font.Font) font.Font { f.Weight = font.Medium; return f }

func colorCall(gtx gl.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// expandHome turns a leading "~" into the home directory.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return home + p[1:]
}

// splitPath cuts what the user typed into the directory to list (expanded,
// home for a bare name) and the name prefix after the last slash.
func splitPath(in string) (dir, prefix string) {
	i := strings.LastIndexByte(in, '/')
	dir, prefix = in[:i+1], in[i+1:]
	if in == "~" {
		dir, prefix = "~/", ""
	}
	dir = expandHome(dir)
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	return dir, prefix
}

// dirMatches lists the subdirectories of in's directory whose names start
// with its last segment, hidden ones only when that segment starts with ".".
func dirMatches(in string) []string {
	dir, prefix := splitPath(in)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, prefix) || strings.HasPrefix(n, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.IsDir() {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// completePath is Tab in the path field: a single match completes to it with
// a trailing slash, several extend to their common prefix. It keeps "~" as
// typed and returns the matches left.
func completePath(in string) (string, []string) {
	if in == "~" {
		in = "~/"
	}
	matches := dirMatches(in)
	if len(matches) == 0 {
		return in, nil
	}
	head := in[:strings.LastIndexByte(in, '/')+1]
	if len(matches) == 1 {
		done := head + matches[0] + "/"
		return done, dirMatches(done)
	}
	common := matches[0]
	for _, m := range matches[1:] {
		for !strings.HasPrefix(m, common) {
			common = common[:len(common)-1]
		}
	}
	return head + common, matches
}

// resolveDir turns the field's text into the absolute folder to add.
// Relative paths are taken from the home directory.
func resolveDir(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errors.New("Type a folder path.")
	}
	p := expandHome(in)
	if !filepath.IsAbs(p) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		return "", errors.New("No folder at " + p)
	case !fi.IsDir():
		return "", errors.New(p + " is a file, not a folder.")
	}
	return p, nil
}
