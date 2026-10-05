package app

import (
	"image"
	"math"
	"slices"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// markSeen clears Unseen on the focused pane's activity in st while the
// window has focus and shows the panes, and tells the daemon once per
// activity. Clearing it here too keeps the ring and the sidebar from
// flashing during the round trip.
func (u *ui) markSeen(st *model.State) {
	f := u.nav.focused()
	if !u.winFocused || u.settings.Shown() || f == "" {
		return
	}
	i := slices.IndexFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == f && a.Unseen })
	if i < 0 {
		return
	}
	st.Activities = slices.Clone(st.Activities) // the backend's slice is shared
	st.Activities[i].Unseen = false
	if u.seeSent == nil {
		u.seeSent = map[string]time.Time{}
	}
	if at := st.Activities[i].UpdatedAt; !u.seeSent[f].Equal(at) {
		u.seeSent[f] = at
		u.send(proto.SeePane{Pane: f})
	}
}

// ring is when a pane's attention ring appeared, for its arrival pulse.
type ring struct {
	updated time.Time // the activity's UpdatedAt
	at      time.Time // the frame it was first drawn in
}

// ringPulse is one beat of the arrival pulse; it beats twice.
const ringPulse = 600 * time.Millisecond

// ringWidth is the ring's width in dp, since after it appeared: 2dp
// steady, swelling to 4dp and back twice first.
func ringWidth(since time.Duration) float32 {
	if since < 0 || since >= 2*ringPulse {
		return 2
	}
	return 2 + 2*float32(math.Sin(math.Pi*float64(since%ringPulse)/float64(ringPulse)))
}

// attentionRing outlines frame in the state color of a, an unseen
// activity: stronger than the focus border, with a short pulse when it
// arrives.
func (u *ui) attentionRing(gtx gl.Context, id string, a model.Activity, frame image.Rectangle, sole bool) {
	if u.rings == nil {
		u.rings = map[string]ring{}
	}
	r := u.rings[id]
	if !r.updated.Equal(a.UpdatedAt) {
		r = ring{a.UpdatedAt, gtx.Now}
		u.rings[id] = r
	}
	since := gtx.Now.Sub(r.at)
	if since < 2*ringPulse {
		gtx.Execute(op.InvalidateCmd{})
	}
	w := float32(gtx.Dp(1)) * ringWidth(since)
	rr := 0
	if !sole {
		rr = gtx.Dp(10)
	}
	// The stroke is centred on the frame's edge and clipped to the frame,
	// so its inner half, w wide, shows.
	defer clip.UniformRRect(frame, rr).Push(gtx.Ops).Pop()
	path := clip.UniformRRect(frame, rr).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, sidebar.StateColor(u.th, a.State), clip.Stroke{Path: path, Width: 2 * w}.Op())
}

// drawAdvice shows a decision model's recommendation for the pane's
// pending approval, s, as a chip in the top-right corner of the pane at r.
func (u *ui) drawAdvice(gtx gl.Context, r layout.Rect, s string) {
	th := u.th
	_, fg := chipColors(th, model.StatePendingApproval)
	call, sz := chip(gtx, th, theme.Mix(th.Surface, fg, 0.16), theme.Mix(th.Surface, fg, 0.4), fg, s)
	if sz.X+gtx.Dp(24) > r.W {
		return
	}
	defer op.Offset(image.Pt(r.X+r.W-sz.X-gtx.Dp(12), r.Y+gtx.Dp(10))).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
}
