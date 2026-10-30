package sidebar

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestProfileCardAndLimits(t *testing.T) {
	st := cardState()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(), Constraints: layout.Exact(image.Pt(288, 800))}
	th := theme.Dark()
	v := newView(gtx, th, st, "", "agent")
	base := cardFor(v, st.Workspaces[0])
	st.Workspaces[0].Profile = "work"
	c := cardFor(v, st.Workspaces[0])
	if c.profile != "work" || !strings.Contains(c.lines(), "e") || strings.Contains(base.lines(), "e") {
		t.Fatalf("card profile %q lines %q", c.profile, c.lines())
	}
	if drawCard(gtx, th, c, 1).Y <= drawCard(gtx, th, base, 1).Y {
		t.Fatal("profile line has no height")
	}
	l := flow.Limit{Provider: model.ProviderClaude, Profile: "work", ProfileLabel: "Work plan"}
	if got := LimitName(l); got != "Claude · Work plan" {
		t.Fatal(got)
	}
	l.ProfileLabel = ""
	if got := LimitName(l); got != "Claude · work" {
		t.Fatal(got)
	}
	s := &Sidebar{Limits: []flow.Limit{{Provider: model.ProviderClaude}, {Provider: model.ProviderCodex}}}
	if s.profileLimitRows() {
		t.Fatal("default meter changed")
	}
	s.Layout(gtx, th, st, "", "agent")
	h1 := s.footer(gtx, v).Size.Y
	s.Limits[0].Profile = "work"
	if !s.profileLimitRows() {
		t.Fatal("profile meter has no labels")
	}
	if got := s.footer(gtx, v).Size.Y; got != h1+gtx.Dp(20) {
		t.Fatalf("profile footer height %d, want %d", got, h1+gtx.Dp(20))
	}
}
