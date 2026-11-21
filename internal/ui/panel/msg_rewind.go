package panel

import (
	"image"
	"image/color"
	"strconv"

	gioevent "gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// confirmAnchor notes where prompt id's Rewind button is, r in the
// element being laid out, while its confirm is open.
func (m *msgState) confirmAnchor(id string, r image.Rectangle) {
	if m.confirm == id {
		m.confirmAt = anchor{el: m.el, r: r.Add(image.Pt(0, m.dy))}
	}
}

// confirmWidth is the rewind confirm's width: wide enough for its three
// buttons in a row.
const confirmWidth = 420

// fileRows is how many files the confirm lists before it scrolls.
const fileRows = 8

// RewindChecked takes the daemon's answer to the ActRewindCheck the open
// confirm asked: what Revert files too would change. An answer for
// another prompt, or a confirm closed meanwhile, is dropped.
func (t *Transcript) RewindChecked(r proto.ChatRewindFiles) {
	if m := &t.msg; m.confirm != "" && r.Turn == m.rewindKey(t.TurnRewind) {
		m.checked = &r
	}
}

// rewindKey is the open confirm's prompt as the daemon names it for a
// rewind: its turn for a chat that rewinds whole turns, else the prompt.
func (m *msgState) rewindKey(turns bool) string {
	if turns {
		return m.confirmTurn
	}
	return m.confirmItem
}

// openConfirm opens the rewind confirm on the prompt it is drawn for and
// asks the daemon what reverting the files would do, where it can.
func (t *Transcript) openConfirm(id, turn string) {
	m := &t.msg
	m.confirm, m.checked = id, nil
	m.files.Position = layout.Position{}
	if t.FilesRewind {
		m.acts = append(m.acts, MessageAction{Kind: ActRewindCheck, Turn: turn, Item: id})
	}
}

// drawConfirm draws the open rewind confirm under its button, over
// everything in the transcript's area of size, x0 the column's left edge,
// as T3 Code's revert dialog: what a rewind does, the files Revert files
// too changes, then Revert and keep changes, Revert files too and Cancel.
// Esc, Cancel or a press outside close it.
func (t *Transcript) drawConfirm(gtx layout.Context, d *cdraw, x0 int, size image.Point) {
	m := &t.msg
	if m.confirm == "" {
		return
	}
	act := func(keep bool) {
		a := MessageAction{Kind: ActRewind, Turn: m.confirmTurn, Item: m.confirmItem, Text: m.confirmText, Attachments: m.confirmAtts, KeepFiles: keep}
		if !keep && m.checked != nil {
			a.Stash = len(m.checked.Unsaved) > 0
		}
		m.acts = append(m.acts, a)
		m.confirm = ""
	}
	for m.no.Clicked(gtx) {
		m.confirm = ""
	}
	for m.ok.Clicked(gtx) {
		act(true)
	}
	for m.revert.Clicked(gtx) {
		if t.canRevert() {
			act(false)
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape}, pointer.Filter{Target: &m.confirmBg, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			m.confirm = ""
		}
	}
	r, ok := m.at(m.confirmAt, x0)
	if m.confirm == "" || !ok {
		return
	}
	th := d.th
	pad := gtx.Dp(theme.SpaceM)
	w := min(gtx.Dp(confirmWidth), size.X-2*gtx.Dp(theme.SpaceS))
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(w-2*pad, size.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		ws := []layout.Widget{
			func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Rewind to here?")
			},
			d.note(th.Muted, t.confirmText()),
		}
		ws = append(ws, t.confirmFiles(d)...)
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return t.confirmButtons(gtx, d)
			})
		})
		return vstack(gtx, gtx.Dp(theme.SpaceS), ws...)
	})
	box := image.Pt(w, dims.Size.Y+2*pad)
	p := kit.Place(r, box, image.Rectangle{Max: size}, kit.BelowStart, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceS))
	rect := image.Rectangle{Min: p, Max: p.Add(box)}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	// It takes the presses on it, so they do not reach the transcript.
	area := clip.Rect(rect).Push(gtx.Ops)
	pointer.CursorDefault.Add(gtx.Ops)
	gioevent.Op(gtx.Ops, &m.confirmBg)
	area.Pop()
	at(gtx, p.Add(image.Pt(pad, pad)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return dims })
}

// confirmText is what the confirm says a rewind does.
func (t *Transcript) confirmText() string {
	s := "The chat goes back to before this message. Your prompt and attachments return to the composer, under anything you were writing."
	if t.FilesRewind {
		return s + " Revert files too also puts the files back as they were then."
	}
	return s + " The files stay as they are."
}

// canRevert reports whether Revert files too can go: the daemon checked
// the files and found no reason to refuse.
func (t *Transcript) canRevert() bool {
	c := t.msg.checked
	return t.FilesRewind && c != nil && c.Refused == "" && len(c.Files) > 0
}

// confirmButtons is the confirm's row: Revert and keep changes, Revert
// files too, which reads Stash and revert files when it stashes first,
// and Cancel.
func (t *Transcript) confirmButtons(gtx layout.Context, d *cdraw) layout.Dimensions {
	m, th := &t.msg, d.th
	bs := []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		return kit.Button(gtx, th, &m.ok, kit.Primary, kit.Medium, "Revert and keep changes")
	}}
	if t.FilesRewind {
		label := "Revert files too"
		if c := m.checked; c != nil && len(c.Unsaved) > 0 {
			label = "Stash and revert files"
		}
		bs = append(bs, func(gtx layout.Context) layout.Dimensions {
			return kit.Button(gtx, th, &m.revert, kit.Danger.When(t.canRevert()), kit.Medium, label)
		})
	}
	bs = append(bs, func(gtx layout.Context) layout.Dimensions {
		return kit.Button(gtx, th, &m.no, kit.Ghost, kit.Medium, "Cancel")
	})
	return kit.Row(gtx, theme.SpaceS, bs...)
}

// confirmFiles is the confirm's account of the files: being checked, why
// they cannot go back, or each file Revert files too changes and each one
// it stashes first.
func (t *Transcript) confirmFiles(d *cdraw) []layout.Widget {
	m, th := &t.msg, d.th
	if !t.FilesRewind {
		return nil
	}
	c := m.checked
	switch {
	case c == nil:
		return []layout.Widget{d.note(th.Muted, "Checking which files would change…")}
	case c.Refused != "":
		return []layout.Widget{d.note(th.Muted, "Revert files too is off: "+c.Refused+".")}
	case len(c.Files) == 0:
		return []layout.Widget{d.note(th.Muted, "The files are already as they were then.")}
	}
	n := len(c.Files)
	ws := []layout.Widget{
		d.note(th.Fg, "Revert files too changes "+nFiles(n)+":"),
		func(gtx layout.Context) layout.Dimensions { return t.fileList(gtx, d) },
	}
	if u := len(c.Unsaved); u > 0 {
		ws = append(ws, d.note(th.Readable(th.Yellow, th.SurfaceElevated),
			nFiles(u)+" changed after the agent's last turn, and no checkpoint holds "+pick(u, "it", "them")+
				": "+names(c.Unsaved)+". Reverting stashes "+pick(u, "it", "them")+
				" first, and pitwall then names the commit that brings everything back."))
	}
	return ws
}

// fileList lists the files a revert changes, each with what happens to
// it, scrolling past fileRows.
func (t *Transcript) fileList(gtx layout.Context, d *cdraw) layout.Dimensions {
	m, th := &t.msg, d.th
	fs := m.checked.Files
	st := d.small(th.Muted)
	rowH := gtx.Sp(st.size) * 3 / 2
	gtx.Constraints.Min.Y = 0
	gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, rowH*min(len(fs), fileRows))
	m.files.Axis = layout.Vertical
	return m.files.Layout(gtx, len(fs), func(gtx layout.Context, i int) layout.Dimensions {
		f := fs[i]
		path := f.Path
		if f.From != "" {
			path = f.From + " → " + f.Path
		}
		gtx.Constraints.Min.Y = rowH
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return widget.Label{MaxLines: 1, Truncator: "…"}.Layout(gtx, th.Shaper, th.MonoFont, st.size, path, material(gtx, th.Fg))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return widget.Label{MaxLines: 1, Alignment: text.End}.Layout(gtx, th.Shaper, th.UIFont, st.size, revertVerb(f.Status), material(gtx, th.Muted))
				})
			}))
	})
}

// revertVerb is what a revert does to a file whose change from the files
// now to the checkpoint is status.
func revertVerb(status string) string {
	switch status {
	case "added", "copied":
		return "restored"
	case "deleted":
		return "removed"
	case "renamed":
		return "renamed back"
	}
	return "reverted"
}

// note is a wrapped small paragraph in color c.
func (d *cdraw) note(c color.NRGBA, s string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		st := d.small(c)
		return widget.Label{}.Layout(gtx, d.th.Shaper, st.f, st.size, s, material(gtx, st.c))
	}
}

// nFiles is "1 file" or "n files".
func nFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// names lists the files' paths, the first few and how many more.
func names(fs []proto.RewindFile) string {
	const most = 3
	s := ""
	for i, f := range fs[:min(len(fs), most)] {
		if i > 0 {
			s += ", "
		}
		s += f.Path
	}
	if len(fs) > most {
		s += " and " + strconv.Itoa(len(fs)-most) + " more"
	}
	return s
}
