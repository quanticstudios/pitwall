package app

import (
	"image"
	"os"
	"strings"
	"sync"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// InstallHooks runs `pitwall hooks install` and returns its report; with
// dry set it changes nothing and reports what would change, one line per
// change. statusline adds `--statusline`. main sets it; nil hides the pane
// notice.
var InstallHooks func(dry, statusline bool) (string, error)

// hooksDialog is the Install hooks dialog: what an install would change,
// then what it did. InstallHooks runs off the UI goroutine.
type hooksDialog struct {
	mu      sync.Mutex
	gen     int  // the latest run; a reply from an older one is dropped
	busy    bool // the latest run has not answered yet
	install bool // the latest run installs; false for the dry run
	text    string
	err     string
	// hidden dismisses the pane notices for the window's lifetime: after
	// Dismiss, or an install, whose hooks the running agents load only
	// when restarted.
	hidden   bool
	reloaded bool // the settings page read the configs again after the install
	// statusline is the box for pitwall statusline, which the dry run and
	// the install add; tick is its clickable.
	statusline bool
	tick       widget.Clickable
}

// hooksNotice reports whether panes whose agent sends no hooks show the
// notice.
func (u *ui) hooksNotice() bool {
	u.hooks.mu.Lock()
	defer u.hooks.mu.Unlock()
	return InstallHooks != nil && !u.hooks.hidden
}

// openHooks shows the dialog and starts its dry run, with the statusline
// box unticked.
func (u *ui) openHooks() { u.openHooksWith(false) }

// openHooksWith is openHooks with the statusline box ticked or not.
func (u *ui) openHooksWith(statusline bool) {
	if InstallHooks == nil {
		return
	}
	u.hooks.mu.Lock()
	u.hooks.statusline = statusline
	u.hooks.mu.Unlock()
	u.modal.open(modalHooks, "")
	u.runHooks(true)
}

func (u *ui) runHooks(dry bool) {
	h := &u.hooks
	h.mu.Lock()
	h.gen++
	gen := h.gen
	h.busy, h.install, h.text, h.err, h.reloaded = true, !dry, "", "", false
	statusline := h.statusline
	h.mu.Unlock()
	go func() {
		out, err := InstallHooks(dry, statusline)
		h.mu.Lock()
		if h.gen == gen {
			h.busy, h.text = false, out
			if err != nil {
				h.err = err.Error()
			}
			h.hidden = h.hidden || !dry && err == nil
		}
		h.mu.Unlock()
		if u.invalidate != nil {
			u.invalidate()
		}
	}()
}

// confirmHooks is the dialog's primary button: Install after the dry
// run, Done after the install.
func (u *ui) confirmHooks() {
	h := &u.hooks
	h.mu.Lock()
	busy, done, failed, text := h.busy, h.install, h.err != "", h.text
	h.mu.Unlock()
	_, _, changed := hookChanges(text)
	switch {
	case busy:
	case done || failed || !changed:
		u.modal.close()
	default:
		u.runHooks(false)
	}
}

// hookChanges groups a dry run's lines, "<path>: <change>", by file in
// order, with the hook command cut from each "added <event>: <command>",
// and reports whether any file would change.
func hookChanges(report string) (files []string, changes map[string][]string, changed bool) {
	changes = map[string][]string{}
	home, _ := os.UserHomeDir()
	for line := range strings.Lines(report) {
		line = strings.TrimSpace(line)
		path, change, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
			path = "~" + path[len(home):]
		}
		if _, seen := changes[path]; !seen {
			files = append(files, path)
		}
		if strings.HasPrefix(change, "added ") {
			change, _, _ = strings.Cut(change, ": ")
		}
		changed = changed || change != "unchanged" && !strings.HasPrefix(change, "skipped: ")
		changes[path] = append(changes[path], change)
	}
	return files, changes, changed
}

func (u *ui) hooksBody(gtx gl.Context) gl.Dimensions {
	th, h := u.th, &u.hooks
	for h.tick.Clicked(gtx) {
		h.mu.Lock()
		h.statusline = !h.statusline
		h.mu.Unlock()
		u.runHooks(true) // what the files would change with the box as it is now
	}
	h.mu.Lock()
	statusline := h.statusline
	busy, installing, text, errText := h.busy, h.install, h.text, h.err
	done := installing && !busy
	reload := done && !h.reloaded
	h.reloaded = h.reloaded || done
	h.mu.Unlock()
	if reload && u.settings.Shown() {
		u.settings.ReloadHooks()
	}
	line := func(f func(gtx gl.Context) gl.Dimensions) gl.FlexChild { return gl.Rigid(f) }
	text14 := func(s string) gl.FlexChild {
		return line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 14, th.Muted, s) })
	}
	kids := []gl.FlexChild{
		line(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, "Install hooks for live status")
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
	}
	ok, cancel := "Install", "Cancel"
	files, changes, changed := hookChanges(text)
	switch {
	case busy && installing:
		kids = append(kids, text14("Installing…"))
	case busy:
		kids = append(kids, text14("Reading the agents' configs…"))
	case errText != "":
		ok, cancel = "Close", ""
		kids = append(kids, line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, 14, th.Red, errText) }))
	case done:
		ok, cancel = "Done", ""
		kids = append(kids, text14("Installed. Restart the agents that are running so they load their hooks. In Codex, run /hooks once to trust them."))
		var backups []string
		for l := range strings.Lines(text) {
			if b, ok := strings.CutPrefix(strings.TrimSpace(l), "Backup: "); ok {
				backups = append(backups, b)
			}
		}
		if len(backups) > 0 {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 12}.Layout), text14("Backups:"))
			for _, b := range backups {
				kids = append(kids, line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, 12, th.Muted, b) }))
			}
		}
	case !changed:
		ok, cancel = "Close", ""
		kids = append(kids, text14("Every agent's hooks are already installed."))
	default:
		kids = append(kids, text14("pitwall adds its entries to these files and keeps a backup of each. The hooks do nothing outside a pitwall pane."))
	}
	if !busy && errText == "" && !done {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 16}.Layout), line(func(gtx gl.Context) gl.Dimensions {
			return u.checkbox(gtx, &h.tick, statusline, "Also show Claude Code's plan limits",
				"Runs Claude Code's status line through pitwall statusline, which saves the 5-hour and weekly limits for Settings, Usage, then prints your own status line as before.")
		}))
		for _, f := range files {
			kids = append(kids,
				gl.Rigid(gl.Spacer{Height: 12}.Layout),
				line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, 12, th.Fg, f) }),
			)
			for _, c := range changes[f] {
				col := theme.Mix(th.Surface, th.Fg, 0.7)
				switch {
				case c == "unchanged":
					col = th.Muted
				case strings.HasPrefix(c, "skipped: "):
					col = th.Yellow
				default:
					c = "+ " + c
				}
				kids = append(kids, line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, 12, col, c) }))
			}
		}
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		line(func(gtx gl.Context) gl.Dimensions { return u.buttons(gtx, cancel, ok, th.Primary, th.OnPrimary) }),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// drawHooksNotice shows, at the bottom of the pane at r, that its agent
// reports no state, with buttons to install the hooks or dismiss.
func (u *ui) drawHooksNotice(gtx gl.Context, p *paneUI, r layout.Rect) {
	for p.hooksInstall.Clicked(gtx) {
		u.openHooks()
	}
	for p.hooksHide.Clicked(gtx) {
		u.hooks.mu.Lock()
		u.hooks.hidden = true
		u.hooks.mu.Unlock()
	}
	th := u.th
	msg, ms := textCall(gtx, th, th.UIFont, 13, th.Fg, "Install hooks for live status")
	install, is := textCall(gtx, th, medium(th.UIFont), 13, th.OnPrimary, "Install")
	hide, hs := textCall(gtx, th, th.UIFont, 13, th.Muted, "Dismiss")
	pad, gap := gtx.Dp(12), gtx.Dp(10)
	bh := ms.Y + gtx.Dp(8)
	iw, hw := is.X+2*gtx.Dp(10), hs.X+2*gtx.Dp(8)
	box := image.Pt(pad+ms.X+gap+iw+gtx.Dp(4)+hw+gtx.Dp(6), bh+2*gtx.Dp(6))
	if box.X+gtx.Dp(24) > r.W {
		return
	}
	at := image.Pt(r.X+(r.W-box.X)/2, r.Y+r.H-box.Y-gtx.Dp(16))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rad := gtx.Dp(8)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
	// why: presses on the notice must not reach the terminal under it.
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &p.hooksBox, Kinds: pointer.Press}); !ok {
			break
		}
	}
	area := clip.Rect{Max: box}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.hooksBox)
	area.Pop()
	o := op.Offset(image.Pt(pad, (box.Y-ms.Y)/2)).Push(gtx.Ops)
	msg.Add(gtx.Ops)
	o.Pop()
	button := func(c *widget.Clickable, x, w int, primary bool, call op.CallOp, ts image.Point) {
		o := op.Offset(image.Pt(x, (box.Y-bh)/2)).Push(gtx.Ops)
		defer o.Pop()
		g := gtx
		g.Constraints = gl.Exact(image.Pt(w, bh))
		c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			rr := clip.UniformRRect(image.Rect(0, 0, w, bh), gtx.Dp(6))
			switch {
			case primary && c.Hovered():
				paint.FillShape(gtx.Ops, theme.Mix(th.Primary, th.Fg, 0.08), rr.Op(gtx.Ops))
			case primary:
				paint.FillShape(gtx.Ops, th.Primary, rr.Op(gtx.Ops))
			case c.Hovered():
				paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
			}
			pointer.CursorPointer.Add(gtx.Ops)
			t := op.Offset(image.Pt((w-ts.X)/2, (bh-ts.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			t.Pop()
			return gl.Dimensions{Size: image.Pt(w, bh)}
		})
	}
	x := pad + ms.X + gap
	button(&p.hooksInstall, x, iw, true, install, is)
	button(&p.hooksHide, x+iw+gtx.Dp(4), hw, false, hide, hs)
}
