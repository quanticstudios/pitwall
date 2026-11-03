package settings

import (
	"slices"
	"strings"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func (p *Page) excluding(gtx gl.Context, n int) gl.Dimensions {
	if n == 0 {
		return gl.Dimensions{}
	}
	return gl.Inset{Top: 8}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Small), p.th.Muted,
			"Excluding "+plural(n, "tab")+" · "+strings.Join(p.s.Stats.Exclude, ", "))
	})
}

func (p *Page) excludeTab(name string) {
	pattern := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(name)
	if name == "" || slices.Contains(p.s.Stats.Exclude, pattern) {
		return
	}
	if err := config.AppendString(p.path(), "stats", "exclude", pattern); err != nil {
		p.err = err.Error()
		return
	}
	p.err, p.result = "", Saved
	p.s.Stats.Exclude = append(slices.Clone(p.s.Stats.Exclude), pattern)
	p.readTimingLog()
	p.readErrorLog()
}
