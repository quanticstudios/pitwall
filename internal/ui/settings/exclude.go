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
	if name == "" || slices.Contains(p.s.Stats.Exclude, name) {
		return
	}
	patterns := append(slices.Clone(p.s.Stats.Exclude), name)
	values := make([]string, len(patterns))
	for i, pattern := range patterns {
		values[i] = config.Quote(pattern)
	}
	p.saveValue("stats", "exclude", "["+strings.Join(values, ", ")+"]")
	if p.err == "" {
		p.s.Stats.Exclude = patterns
		p.readTimingLog()
		p.readErrorLog()
	}
}
