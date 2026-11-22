package settings

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const ghosttyDD = "ghostty-import"

type pickResult struct {
	path string
	err  error
}

// themeImport is the inline Ghostty/iTerm2 theme importer on Settings, Appearance.
type themeImport struct {
	open    bool
	focus   bool
	src     widget.Editor
	ghostty []string // installed Ghostty themes found on this machine
	picking bool
	picked  chan pickResult
	err     string
	note    string
}

// pickThemeFileFn opens a native file chooser; replaced in tests.
var pickThemeFileFn = pickThemeFile

func pickThemeFile() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "osascript", "-e", `POSIX path of (choose file with prompt "Import Ghostty or iTerm2 theme")`).Output()
		if err != nil {
			return "", nil // user cancelled
		}
		return strings.TrimSpace(string(out)), nil
	case "windows":
		script := `Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.OpenFileDialog; $d.Title = 'Import theme'; $d.Filter = 'All files (*.*)|*.*|iTerm2 themes (*.itermcolors)|*.itermcolors'; if ($d.ShowDialog() -eq 'OK') { Write-Output $d.FileName }`
		out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	default:
		if bin, err := exec.LookPath("zenity"); err == nil {
			out, err := exec.CommandContext(ctx, bin, "--file-selection", "--title=Import theme").Output()
			if err != nil {
				return "", nil // user cancelled
			}
			return strings.TrimSpace(string(out)), nil
		}
		if bin, err := exec.LookPath("kdialog"); err == nil {
			out, err := exec.CommandContext(ctx, bin, "--getopenfilename", ".", "All files (*)").Output()
			if err != nil {
				return "", nil
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", errors.New("no file dialog (zenity or kdialog) found; paste a file path or Ghostty theme name")
	}
}

func (p *Page) startPickThemeFile(gtx gl.Context) {
	if p.im.picking {
		return
	}
	p.im.picking = true
	p.im.err = ""
	ch := make(chan pickResult, 1)
	p.im.picked = ch
	go func() {
		path, err := pickThemeFileFn()
		ch <- pickResult{path: path, err: err}
	}()
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(150 * time.Millisecond)})
}

func (p *Page) pollThemePick(gtx gl.Context) {
	if !p.im.picking || p.im.picked == nil {
		return
	}
	select {
	case res := <-p.im.picked:
		p.im.picking = false
		p.im.picked = nil
		if res.err != nil {
			p.im.err = res.err.Error()
		} else if res.path != "" {
			p.im.src.SetText(res.path)
			p.runThemeImport(res.path)
		}
		gtx.Execute(op.InvalidateCmd{})
	default:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(150 * time.Millisecond)})
	}
}

// runThemeImport imports source into the config dir's themes folder, refreshes
// the page's theme list, and applies a single imported theme when not
// following the system appearance.
func (p *Page) runThemeImport(source string) {
	themesDir := filepath.Join(filepath.Dir(p.path()), "themes")
	res, err := config.ImportTheme(source, "", themesDir)
	if err != nil {
		p.im.err = err.Error()
		p.im.note = ""
		return
	}
	p.ReloadThemes()
	p.im.err = ""
	p.im.open = false
	p.im.src.SetText("")
	if len(res) == 1 {
		if p.s.FollowSystem {
			p.im.note = fmt.Sprintf("Imported %s. Pick it for light or dark mode below.", res[0].Name)
		} else {
			p.im.note = fmt.Sprintf("Imported and applied %s.", res[0].Name)
			p.saveValue("theme", "name", config.Quote(res[0].Name))
		}
	} else {
		p.im.note = fmt.Sprintf("Imported %d themes.", len(res))
	}
}

func (p *Page) importThemeControl(gtx gl.Context) gl.Dimensions {
	c := p.btn("theme-import-open")
	for c.Clicked(gtx) {
		p.im.open = !p.im.open
		p.im.err = ""
		if p.im.open {
			p.im.focus = true
			p.im.note = ""
			p.dd = ""
		}
		gtx.Execute(op.InvalidateCmd{})
	}
	label := "Import theme"
	if p.im.open {
		label = "Close"
	}
	return p.button(gtx, c, secondary, label)
}

func (p *Page) importThemeBelowWidget() gl.Widget {
	if !p.im.open && p.im.note == "" && !p.im.picking {
		return nil
	}
	return p.importThemeBelow
}

func (p *Page) importThemeBelow(gtx gl.Context) gl.Dimensions {
	th := p.th
	p.pollThemePick(gtx)
	if !p.im.open {
		if p.im.note == "" {
			return gl.Dimensions{}
		}
		return p.para(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, p.im.note)
	}
	p.im.src.SingleLine, p.im.src.Submit = true, true
	for {
		ev, ok := p.im.src.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			p.runThemeImport(p.im.src.Text())
			gtx.Execute(op.InvalidateCmd{})
		}
	}
	if p.im.focus {
		p.im.focus = false
		gtx.Execute(key.FocusCmd{Tag: &p.im.src})
	}
	browse := p.btn("theme-import-browse")
	for browse.Clicked(gtx) {
		p.startPickThemeFile(gtx)
	}
	doImport := p.btn("theme-import-submit")
	for doImport.Clicked(gtx) {
		p.runThemeImport(p.im.src.Text())
		gtx.Execute(op.InvalidateCmd{})
	}
	browseKind := secondary
	if p.im.picking {
		browseKind = secondary.Disabled()
	}
	importKind := primary.When(strings.TrimSpace(p.im.src.Text()) != "")

	var kids []gl.FlexChild
	kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				return p.field(gtx, &p.im.src, "Path to Ghostty or .itermcolors file, folder, or Ghostty theme name", false)
			}),
			gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				label := "Browse…"
				if p.im.picking {
					label = "Choosing…"
				}
				return p.button(gtx, browse, browseKind, label)
			}),
			gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.button(gtx, doImport, importKind, "Import")
			}),
		)
	}))
	if len(p.im.ghostty) > 0 {
		kids = append(kids,
			gl.Rigid(gl.Spacer{Height: theme.SpaceS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return hstack(gtx, theme.SpaceS,
					func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "Or pick from installed Ghostty themes:")
					},
					p.ghosttyPicker,
				)
			}),
		)
	}
	if p.im.err != "" {
		kids = append(kids,
			gl.Rigid(gl.Spacer{Height: theme.SpaceXS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, th.Sp(theme.Small), th.Red, p.im.err)
			}),
		)
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

func (p *Page) ghosttyPicker(gtx gl.Context) gl.Dimensions {
	th := p.th
	c := p.btn("dd:" + ghosttyDD)
	for c.Clicked(gtx) {
		if p.dd == ghosttyDD {
			p.dd = ""
		} else {
			p.dd, p.ddFocus, p.ddList.Position, p.conflict, p.rec = ghosttyDD, true, gl.Position{}, nil, slot{}
			p.ddFilter.SetText("")
		}
	}
	w, h := gtx.Dp(220), gtx.Dp(32)
	d := c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		border := th.Border
		if c.Hovered() || p.dd == ghosttyDD {
			border = theme.Mix(th.Border, th.Fg, 0.25)
		}
		if gtx.Focused(c) {
			kit.FocusRing(gtx, th, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
		}
		rrect(gtx, border, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
		rrect(gtx, th.SurfaceElevated, image.Rect(1, 1, w-1, h-1), gtx.Dp(theme.RadiusControl)-1)
		g := gtx
		g.Constraints.Max.X = w - gtx.Dp(40)
		m := op.Record(gtx.Ops)
		td := p.text(g, th.UIFont, th.Sp(theme.Body), th.Muted, fmt.Sprintf("Browse %d themes…", len(p.im.ghostty)))
		call := m.Stop()
		o := op.Offset(image.Pt(gtx.Dp(10), (h-td.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		is := gtx.Dp(14)
		o = op.Offset(image.Pt(w-gtx.Dp(10)-is, (h-is)/2)).Push(gtx.Ops)
		icon(gtx, th.Muted, is, chevronGlyph)
		o.Pop()
		defer clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: image.Pt(w, h)}
	})
	if p.dd == ghosttyDD {
		p.ghosttyPopover(gtx, w, h)
	}
	return d
}

func (p *Page) ghosttyPopover(gtx gl.Context, w, top int) {
	th := p.th
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.ddTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			p.dd = ""
		}
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &p.ddPanel, Kinds: pointer.Press}); !ok {
			break
		}
	}
	q := strings.ToLower(strings.TrimSpace(p.ddFilter.Text()))
	var shown []string
	for _, name := range p.im.ghostty {
		if strings.Contains(strings.ToLower(name), q) {
			shown = append(shown, name)
		}
	}
	pick := func(name string) {
		p.dd = ""
		p.runThemeImport(name)
		gtx.Execute(op.InvalidateCmd{})
	}
	p.ddFilter.SingleLine, p.ddFilter.Submit = true, true
	for {
		ev, ok := p.ddFilter.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			p.ddList.Position = gl.Position{}
		case widget.SubmitEvent:
			if len(shown) > 0 {
				pick(shown[0])
			}
		}
	}
	for i, name := range shown {
		if i > 400 {
			break
		}
		for p.btn("ghostty:" + name).Clicked(gtx) {
			pick(name)
		}
	}
	if p.dd != ghosttyDD {
		return
	}
	if p.ddFocus {
		p.ddFocus = false
		gtx.Execute(key.FocusCmd{Tag: &p.ddFilter})
	}

	m := op.Record(gtx.Ops)
	bg := clip.Rect{Min: image.Pt(-1<<14, -1<<14), Max: image.Pt(1<<14, 1<<14)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddTag)
	bg.Pop()
	itemH, pad := gtx.Dp(28), gtx.Dp(6)
	listH := min(gtx.Dp(260), max(1, len(shown))*itemH)
	panel := image.Rect(0, 0, max(w, gtx.Dp(260)), pad+gtx.Dp(32)+pad+listH+pad)
	o := op.Offset(image.Pt(0, top+gtx.Dp(4))).Push(gtx.Ops)
	kit.Surface(gtx, panel, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	pa := clip.Rect(panel).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddPanel)
	pa.Pop()

	fo := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	fg := gtx
	fg.Constraints = gl.Exact(image.Pt(panel.Dx()-2*pad, gtx.Dp(32)))
	p.field(fg, &p.ddFilter, "Filter Ghostty themes", true)
	fo.Pop()

	lo := op.Offset(image.Pt(pad, pad+gtx.Dp(32)+pad)).Push(gtx.Ops)
	lg := gtx
	lg.Constraints = gl.Exact(image.Pt(panel.Dx()-2*pad, listH))
	cl := clip.Rect{Max: lg.Constraints.Max}.Push(gtx.Ops)
	if len(shown) == 0 {
		gl.Center.Layout(lg, func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "No Ghostty theme matches")
		})
	} else {
		p.ddList.Axis = gl.Vertical
		p.ddList.Layout(lg, min(len(shown), 401), func(gtx gl.Context, i int) gl.Dimensions {
			name := shown[i]
			c := p.btn("ghostty:" + name)
			return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				sz := image.Pt(gtx.Constraints.Max.X, itemH)
				if c.Hovered() {
					rrect(gtx, theme.Mix(th.SurfaceElevated, th.Fg, 0.06), image.Rectangle{Max: sz}, gtx.Dp(4))
				}
				g := gtx
				g.Constraints.Max.X = sz.X - gtx.Dp(16)
				mm := op.Record(gtx.Ops)
				d := p.text(g, th.UIFont, th.Sp(theme.Body), th.Fg, name)
				call := mm.Stop()
				oo := op.Offset(image.Pt(gtx.Dp(8), (itemH-d.Size.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				oo.Pop()
				defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: sz}
			})
		})
	}
	cl.Pop()
	lo.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
