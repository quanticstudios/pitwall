package settings

import (
	"slices"
	"strings"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The Keyboard shortcuts page has its own search, as VS Code's keybinding
// editor does: it filters only shortcuts and keeps their sections. Typed
// keys such as "ctrl+shift+d" find that chord (see config.KeyQuery), and
// Record finds the next chord pressed, in any table.

// keyFilter is secs with only the rows q matches, or with c set, the rows
// bound to c. A section left empty goes.
func keyFilter(secs []section, q string, c config.Chord) []section {
	if q == "" && c.Name == "" {
		return secs
	}
	var out []section
	for _, sec := range secs {
		rs := filterRows(sec.rows, q)
		if c.Name != "" {
			rs = slices.DeleteFunc(slices.Clone(sec.rows), func(r row) bool { return !slices.Contains(r.keys, c) })
		}
		if len(rs) > 0 {
			sec.rows = rs
			out = append(out, sec)
		}
	}
	return out
}

// noMatch is what a search for q (or the chord c) that found nothing says.
func noMatch(q string, c config.Chord, what string) string {
	if c.Name == "" {
		c, _ = config.KeyQuery(q)
	}
	if c.Name != "" {
		return "Nothing uses " + c.String() + "."
	}
	return what
}

// keyResults is the shortcuts' sections the page's search shows, and
// what it says when there are none.
func (p *Page) keyResults() ([]section, string) {
	q := strings.TrimSpace(p.keySearch.Text())
	secs := keyFilter(p.sections(catKeys), q, p.keyChord)
	return secs, noMatch(q, p.keyChord, "No shortcuts match. Search by name, by keys such as ctrl+shift+d, or with Record keys.")
}

// toggleKeyRec turns Record on or off.
func (p *Page) toggleKeyRec() {
	if p.keyRec {
		p.keyRec = false
		return
	}
	p.startRecord(slot{})
	p.keyRec = true
}

// keyRecorded is a chord pressed while Record is on: it becomes the search.
// Escape stops recording instead.
func (p *Page) keyRecorded(c config.Chord) {
	if c.Mods == 0 && c.Name == key.NameEscape {
		p.keyRec = false
		return
	}
	p.keyChord = c
	p.keySearch.SetText(c.String())
	p.list.Position = gl.Position{}
}

// keyBar is the page's search field and Record, with a line under them
// while recording.
func (p *Page) keyBar(gtx gl.Context) gl.Dimensions {
	th := p.th
	for {
		ev, ok := p.keySearch.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.ChangeEvent); ok && p.keySearch.Text() != p.keyChord.String() {
			p.keyChord, p.list.Position = config.Chord{}, gl.Position{}
		}
	}
	p.keySearch.SingleLine = true
	rec := p.btn("key-record")
	for rec.Clicked(gtx) {
		p.toggleKeyRec()
	}
	if p.focusKeys {
		p.focusKeys = false
		gtx.Execute(key.FocusCmd{Tag: &p.keySearch})
	}
	bar := func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				return p.field(gtx, &p.keySearch, "Search shortcuts, or type keys like ctrl+shift+d", true)
			}),
			gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				if p.keyRec {
					return kit.ButtonHint(gtx, th, rec, primary, kit.Large, "Recording", "Esc")
				}
				return kit.Button(gtx, th, rec, secondary, kit.Large, "Record keys")
			}),
		)
	}
	if !p.keyRec {
		return bar(gtx)
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(bar),
		gl.Rigid(gl.Spacer{Height: theme.SpaceS}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, th.Sp(theme.Small), th.Primary,
				"Press a shortcut to see what it runs, in every mode. Keys your desktop keeps for itself never get here: type those instead.")
		}),
	)
}
