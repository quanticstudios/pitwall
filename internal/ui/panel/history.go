package panel

import (
	"image"
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A long chat comes as its newest items; the reader scrolling near the
// top reads the page before (Transcript.Older), which lands above the
// rows in view without moving them. The parsed replies, diffs and text
// selections of rows far from the view go once there are many, so
// scrolling through a long history holds only what is near the view.
const (
	olderRows = 20  // how near the top, in rows, the page before is read
	keepRows  = 100 // rows either side of the view whose laid-out text stays
	keepLaid  = 400 // laid-out texts held before those far from the view go
	keepFor   = 30  // frames a selectable stays undrawn before it goes
)

// prepended is how many items came before the oldest item of the last
// frame, and moves the answered requests' lines down by as many.
func (t *Transcript) prepended(s *session.Snapshot) int {
	if len(s.Content) == 0 {
		t.head = ""
		return 0
	}
	n := 0
	if t.head != "" && s.Content[0].ID != t.head {
		n = max(0, slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == t.head }))
	}
	t.head = s.Content[0].ID
	for i := range t.done {
		t.done[i].at += n
	}
	return n
}

// keepPlace puts the row of the item that was first in view first again,
// after a page came before it, at the same offset.
func (t *Transcript) keepPlace(s *session.Snapshot) {
	a := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == t.anchor })
	if a < 0 {
		return
	}
	at := -1
	for i, r := range t.rows {
		if rowItem(r) == a {
			at = i
			break
		}
		if at < 0 && r.tail == nil && r.res == nil && r.seg != segItem && r.b.lo <= a && a < r.b.hi {
			at = i // a turn the page made longer at its start
		}
	}
	if at < 0 {
		return
	}
	if t.list.Position.First == 0 { // the space above the rows was in view: it is above the anchor now
		t.list.Position.Offset -= t.topH
	}
	t.list.Position.First = at + 1 // the list's first element is the space above the rows
}

// atTop notes the item first in view, and reads the page before the
// oldest item when the view is near it: once the list has gone to its end
// (it lays out from the top before), or when it does not fill the view
// of height h.
func (t *Transcript) atTop(s *session.Snapshot, h int) {
	f := max(0, t.list.Position.First-1) // the space above the rows stands for the first
	if f < len(t.rows) {
		if i := rowItem(t.rows[f]); i >= 0 && i < len(s.Content) {
			t.anchor = s.Content[i].ID
		}
	}
	settled := t.list.ScrollToEnd || t.list.Position.Length <= h
	if t.Older != nil && settled && t.list.Position.First < olderRows {
		t.older = t.Older()
	}
}

// rowItem is the item row r draws, or starts with for a prompt or a work
// log's header, -1 for none.
func rowItem(r trow) int {
	switch {
	case r.tail != nil || r.res != nil:
		return -1
	case r.seg == segItem:
		return r.i
	case r.seg == segReply:
		return r.b.reply
	}
	return r.b.lo
}

// wantWhole reads the cut items row r lays out whole.
func (t *Transcript) wantWhole(s *session.Snapshot, r *trow) {
	if t.Full == nil || r.tail != nil || r.res != nil {
		return
	}
	lo, hi := r.b.lo, r.b.hi
	switch r.seg {
	case segItem:
		lo, hi = r.i, r.i+1
	case segReply:
		lo, hi = r.b.reply, r.b.reply+1
	case segHead:
		return // a folded work log shows its items' summaries alone
	}
	for i := max(0, lo); i < min(hi, len(s.Content)); i++ {
		if s.Content[i].Cut {
			t.Full(s.Content[i].ID)
		}
	}
}

// forget drops the laid-out text of rows far from the view once there is
// much of it: parsed replies and diffs by item, and selectables not drawn
// for keepFor frames but the one holding the selection.
func (t *Transcript) forget(s *session.Snapshot) {
	if len(t.sels) > keepLaid {
		for k, sl := range t.sels {
			if t.frame-t.selAt[k] > keepFor && sl != t.picked {
				delete(t.sels, k)
				delete(t.selAt, k)
				delete(t.tool.syn, strings.TrimPrefix(k, "box:"))
			}
		}
	}
	if len(t.docs)+len(t.diffs) <= keepLaid {
		return
	}
	near := map[string]bool{}
	from := max(0, t.list.Position.First-1-keepRows)
	to := min(len(t.rows), t.list.Position.First+t.list.Position.Count+keepRows)
	for _, r := range t.rows[from:max(from, to)] {
		lo, hi := r.b.lo, r.b.hi
		if r.seg == segItem {
			lo, hi = r.i, r.i+1
		}
		for i := max(0, lo); r.tail == nil && r.res == nil && i < min(hi, len(s.Content)); i++ {
			near[s.Content[i].ID] = true
		}
	}
	for id := range t.docs {
		if !near[id] {
			delete(t.docs, id)
		}
	}
	for id := range t.diffs {
		if !near[id] {
			delete(t.diffs, id)
		}
	}
}

// loadingOlder is the list's first row while there is history before
// the oldest item: a line saying it loads, as the space above the rows.
func (t *Transcript) loadingOlder(gtx layout.Context, th *theme.Theme, w int) layout.Dimensions {
	pad := gtx.Dp(theme.SpaceXL)
	gtx.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
	call, d := measure(gtx, func(gtx layout.Context) layout.Dimensions { return kit.Loading(gtx, th, "Loading earlier messages…") })
	at(gtx, image.Pt(0, pad), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return d })
	return layout.Dimensions{Size: image.Pt(w, d.Size.Y+pad)}
}
