package app

import (
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
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
	reloaded bool      // the settings page read the configs again after the install
	shownAt  time.Time // when the pane notices first showed, for their fade; UI goroutine only
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
		return line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, s) })
	}
	kids := []gl.FlexChild{
		line(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Install hooks for live status")
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
		kids = append(kids, line(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Red, errText)
		}))
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
				kids = append(kids, line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Muted, b) }))
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
				line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Fg, f) }),
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
				kids = append(kids, line(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.MonoFont, th.Sp(theme.Small), col, c) }))
			}
		}
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		line(func(gtx gl.Context) gl.Dimensions { return u.buttons(gtx, cancel, ok, kit.Primary) }),
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
	msg, ms := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, "Install hooks for live status")
	rec := func(c *widget.Clickable, k kit.Kind, label string) (op.CallOp, image.Point) {
		m := op.Record(gtx.Ops)
		d := kit.Button(gtx, th, c, k, kit.Small, label)
		return m.Stop(), d.Size
	}
	install, is := rec(&p.hooksInstall, kit.Primary, "Install")
	hide, hs := rec(&p.hooksHide, kit.Ghost, "Dismiss")
	pad, gap := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceM)
	box := image.Pt(pad+ms.X+gap+is.X+gtx.Dp(theme.SpaceXS)+hs.X+gtx.Dp(theme.SpaceS), max(ms.Y, is.Y)+2*gtx.Dp(theme.SpaceS))
	if box.X+gtx.Dp(theme.SpaceXL) > r.W {
		return
	}
	at := image.Pt(r.X+(r.W-box.X)/2, r.Y+r.H-box.Y-gtx.Dp(theme.SpaceL))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	defer paint.PushOpacity(gtx.Ops, anim.At(gtx, u.hooksShownAt(gtx), anim.Fade)).Pop()
	rad := gtx.Dp(theme.RadiusPopover)
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
	x := pad + ms.X + gap
	o = op.Offset(image.Pt(x, (box.Y-is.Y)/2)).Push(gtx.Ops)
	install.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(x+is.X+gtx.Dp(theme.SpaceXS), (box.Y-hs.Y)/2)).Push(gtx.Ops)
	hide.Add(gtx.Ops)
	o.Pop()
}

// hooksShownAt is when the pane notices first showed, for their fade.
func (u *ui) hooksShownAt(gtx gl.Context) time.Time {
	if u.hooks.shownAt.IsZero() {
		u.hooks.shownAt = gtx.Now
	}
	return u.hooks.shownAt
}
