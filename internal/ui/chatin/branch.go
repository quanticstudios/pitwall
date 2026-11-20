package chatin

import (
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// copiedFor is how long a copy button shows its check.
const copiedFor = 1500 * time.Millisecond

// branchChip is the composer row's branch: a click copies it.
type branchChip struct {
	b      widget.Clickable
	copied time.Time // when a click copied it
}

// branchEvents copies the branch on a click.
func (c *Composer) branchEvents(gtx layout.Context, env Env) {
	if c.branch.b.Clicked(gtx) && env.Branch != "" && env.Copy != nil {
		env.Copy(env.Branch)
		c.branch.copied = gtx.Now
	}
}

// Copied reports whether a copy made at at still shows its check at now.
func Copied(at, now time.Time) bool { return !at.IsZero() && now.Sub(at) < copiedFor }

// branchLayout is the branch chip, nothing outside a repo: the branch
// icon and name, its check for copiedFor after a click.
func (c *Composer) branchLayout(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	if env.Branch == "" {
		return layout.Dimensions{}
	}
	icon := "git-branch"
	if Copied(c.branch.copied, gtx.Now) {
		icon = "check"
		gtx.Execute(op.InvalidateCmd{At: c.branch.copied.Add(copiedFor)})
	}
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(220))
	return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := c.pick(gtx, th, &c.branch.b, icon, env.Branch, pickFg(th), false)
		c.tip(gtx, th, &c.branch.b, d.Size, "Copy branch name", false)
		return d
	})
}
