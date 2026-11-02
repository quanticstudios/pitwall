package sidebar

// The inbox: every agent tab of the session in one list, in sections by
// what it needs from the user. The sections, the drop verbs and the
// recede rule follow T3 Code's sidebar (MIT), written anew for pitwall.

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strconv"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// SetInbox settles or unsettles a tab and snoozes it until SnoozedUntil,
// zero to wake it: proto.SetTabInbox.
type SetInbox struct {
	WorkspaceID  string
	Settled      bool
	SnoozedUntil time.Time
}

// SetView is a click on the header's inbox button: show the inbox, or
// the tree with Inbox false. The sidebar has switched already; the window
// keeps the choice in config.toml.
type SetView struct{ Inbox bool }

const (
	icInbox   = "M22 12h-6l-2 3h-4l-2-3H2M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"
	icSettle  = "M18 6 7 17l-5-5M22 10l-7.5 7.5L13 16" // lucide check-check
	recede    = 0.55                                   // a receded row's opacity
	undoFor   = 8 * time.Second                        // how long the undo notice stays
	sectionH  = 28                                     // a section header's height, dp
	emptyRowH = 32                                     // an empty section's placeholder, dp
)

// section is where a tab sits in the inbox, in drawing order.
type section int

const (
	secPinned section = iota
	secActive
	secWorking
	secSnoozed
	secSettled
	secShells // tabs without an agent, quiet at the end
	secCount
)

var sectionTitle = [secCount]string{"Pinned", "Active", "Working", "Snoozed", "Settled", "Shells"}

// sectionEmpty is what an empty section says while it shows: Active
// always, Pinned and Settled while a tab is dragged.
var sectionEmpty = [secCount]string{"Drop a tab here to pin it", "No tab needs you", "", "", "Drop a tab here to settle it", ""}

// status is what an inbox row says its agent needs, least urgent first.
type status int

const (
	statusReady status = iota
	statusFailed
	statusLimited // stopped by a usage limit
	statusWorking
	statusInput // a question or a plan
	statusApproval
)

// statusOne is one activity's status.
func statusOne(a model.Activity, now time.Time) status {
	switch a.State {
	case model.StatePendingApproval:
		return statusApproval
	case model.StateAwaitingInput, model.StatePlanReady:
		return statusInput
	case model.StateWorking, model.StateConnecting, model.StateWatching:
		return statusWorking
	case model.StateError:
		if _, _, ok := agent.UsageLimit(a.Detail, now); ok || !a.Resumes.IsZero() {
			return statusLimited
		}
		return statusFailed
	}
	return statusReady
}

// statusOf is the status a tab's row shows for its agents' activities,
// in the order approval, input, working, failed or usage limit, ready,
// and the activity that says so; a ready tab shows its most recent.
func statusOf(acts []model.Activity, now time.Time) (status, *model.Activity) {
	best, at := statusReady, model.Aggregate(acts)
	for i := range acts {
		if st := statusOne(acts[i], now); st > best || st == statusLimited && best == statusFailed {
			best, at = st, &acts[i]
		}
	}
	return best, at
}

// sectionOf is the section of tab w: shells apart, then snoozed, settled,
// pinned, working (with the shelf on), the rest active. A tab you put
// somewhere stays there while its agent works.
func sectionOf(w model.Workspace, isAgent bool, st status, now time.Time, shelf bool) section {
	switch {
	case !isAgent:
		return secShells
	case w.SnoozedUntil.After(now):
		return secSnoozed
	case !w.Settled.IsZero():
		return secSettled
	case w.Pinned:
		return secPinned
	case shelf && st == statusWorking:
		return secWorking
	}
	return secActive
}

// needRank sorts the Active section: a request or a failure first, then
// a finished turn not seen yet, then work, then the rest.
func needRank(st status, unseen bool) int {
	switch {
	case st >= statusInput || st == statusFailed || st == statusLimited:
		return 0
	case unseen:
		return 1
	case st == statusWorking:
		return 2
	}
	return 3
}

// recedes reports whether a row dims: working, or ready and seen, unless
// it is the open tab or selected. Approval and input never recede.
func recedes(st status, unseen, active, selected bool) bool {
	if active || selected {
		return false
	}
	return st == statusWorking || st == statusReady && !unseen
}

// verb is what dropping a row into another section does to it.
type verb int

const (
	verbNone verb = iota
	verbPin
	verbUnpin
	verbSettle
	verbUnsettle
	verbWake
)

var verbText = [...]string{"", "Pinned", "Unpinned", "Settled", "Unsettled", "Woke"}

// dropVerb is what a drop from section from into to does. Working fills
// itself, Snoozed takes a time from the menu, and shells stay shells.
func dropVerb(from, to section) verb {
	switch {
	case to == from, to == secWorking, to == secSnoozed, to == secShells, from == secShells:
		return verbNone
	case to == secPinned:
		return verbPin
	case to == secSettled:
		return verbSettle
	case from == secPinned:
		return verbUnpin
	case from == secSettled:
		return verbUnsettle
	case from == secSnoozed:
		return verbWake
	}
	return verbNone
}

// shelf is a tab's inbox flags.
type shelf struct {
	pinned, settled bool
	until           time.Time // snoozed until; zero for awake
}

func shelfOf(w model.Workspace) shelf {
	return shelf{w.Pinned, !w.Settled.IsZero(), w.SnoozedUntil}
}

// apply is sh after vb: the tab lands in the section vb names, Active for
// unpin, unsettle and wake.
func (sh shelf) apply(vb verb) shelf {
	switch vb {
	case verbPin:
		return shelf{pinned: true}
	case verbSettle:
		return shelf{pinned: sh.pinned, settled: true}
	case verbUnpin, verbUnsettle, verbWake:
		return shelf{}
	}
	return sh
}

// shelfEvents are the events that take tab w to shelf to.
func shelfEvents(w model.Workspace, to shelf) []Event {
	var out []Event
	if w.Pinned != to.pinned {
		out = append(out, SetTabOptions{WorkspaceID: w.ID, Pinned: to.pinned, Color: w.Color})
	}
	if from := shelfOf(w); from.settled != to.settled || !from.until.Equal(to.until) {
		out = append(out, SetInbox{WorkspaceID: w.ID, Settled: to.settled, SnoozedUntil: to.until})
	}
	return out
}

// snoozePreset is a wake time the Snooze menu offers.
type snoozePreset struct {
	label, when string
	at          time.Time
}

// snoozePresets are the wake times from now, soonest first: in an hour,
// this evening at 18:00 while it is ahead, tomorrow at 9:00 and Monday at
// 9:00, one per minute, so Monday drops out on a Sunday.
func snoozePresets(now time.Time) []snoozePreset {
	day := func(d, h int) time.Time {
		y, m, dd := now.Date()
		return time.Date(y, m, dd+d, h, 0, 0, 0, now.Location()) // a calendar day, DST or not
	}
	monday := (8 - int(now.Weekday())) % 7
	if monday == 0 {
		monday = 7
	}
	cands := []snoozePreset{
		{label: "In an hour", at: now.Add(time.Hour).Truncate(time.Minute)},
		{label: "This evening", at: day(0, 18)},
		{label: "Tomorrow morning", at: day(1, 9)},
		{label: "Monday", at: day(monday, 9)},
	}
	slices.SortStableFunc(cands, func(a, b snoozePreset) int { return a.at.Compare(b.at) })
	var out []snoozePreset
	for _, c := range cands {
		if !c.at.After(now) || len(out) > 0 && out[len(out)-1].at.Equal(c.at) {
			continue
		}
		c.when = c.at.Format("15:04") // the label names the day
		if c.label == "In an hour" {
			c.when = wakeText(now, c.at) // past midnight it is tomorrow
		}
		out = append(out, c)
	}
	return out
}

// wakeText is when a snooze ends: "18:00" today, else "Mon 09:00".
func wakeText(now, at time.Time) string {
	at = at.In(now.Location())
	if y, m, d := now.Date(); at.Year() == y && at.Month() == m && at.Day() == d {
		return at.Format("15:04")
	}
	return at.Format("Mon 15:04")
}

// toInbox sorts v's tabs into the inbox's sections and makes each row's
// activity the one its status names. A worker has no row of its own: it
// rides under its parent's, which sorts by the most urgent of the two.
func (s *Sidebar) toInbox(v *view) {
	v.inbox = true
	v.secOf, v.need = map[string]section{}, map[string]status{}
	for _, ws := range v.st.Ordered(v.session) {
		if _, live := v.activity[ws.ID]; !live || v.rootOf[ws.ID] != "" {
			continue // detached, or a worker
		}
		acts, isAgent := v.family(ws.ID)
		st, a := statusOf(acts, v.now)
		if a != nil {
			v.activity[ws.ID] = a
		}
		sec := sectionOf(ws, isAgent, st, v.now, s.WorkingShelf)
		v.secOf[ws.ID], v.need[ws.ID] = sec, st
		v.sections[sec] = append(v.sections[sec], ws.ID)
	}
	ws := func(id string) model.Workspace { return *v.st.Workspace(id) }
	slices.SortStableFunc(v.sections[secActive], func(a, b string) int {
		return needRank(v.need[a], v.unseen[a] != nil) - needRank(v.need[b], v.unseen[b] != nil)
	})
	slices.SortStableFunc(v.sections[secSnoozed], func(a, b string) int { return ws(a).SnoozedUntil.Compare(ws(b).SnoozedUntil) })
	slices.SortStableFunc(v.sections[secSettled], func(a, b string) int { return ws(b).Settled.Compare(ws(a).Settled) })
}

// family is the activities of tab id and its workers, and whether any of
// them runs an agent.
func (v *view) family(id string) ([]model.Activity, bool) {
	acts, isAgent := v.acts[id], v.agent[id] != ""
	for _, k := range v.kids[id] {
		acts = append(slices.Clip(acts), v.acts[k.ID]...)
		isAgent = isAgent || v.agent[k.ID] != ""
	}
	return acts, isAgent
}

// inboxOrder is the inbox's tabs top to bottom.
func inboxOrder(v *view) []string {
	var out []string
	for _, ids := range v.sections {
		out = append(out, ids...)
	}
	return out
}

// droppable reports whether a dragged tab can land in sec.
func droppable(sec section) bool { return sec == secPinned || sec == secActive || sec == secSettled }

// placeInbox lays the inbox out as place does the tree: each section that
// shows as a header elem ('h', its id the section's number) whose bottom
// takes in the empty placeholder, then its rows with their sub-rows, then
// the queued tasks. It replaces the tree's queue blocks, whose indexes
// point into the tree's elems.
func (s *Sidebar) placeInbox(gtx layout.Context, v *view) ([]elem, int) {
	var out []elem
	s.queue = s.queue[:0]
	rowH := rowHeight(gtx, v.th)
	dragging := s.drag.active && s.drag.kind == 's'
	y := 0
	for sec := range secCount {
		ids := v.sections[sec]
		if len(ids) == 0 && sec != secActive && !(dragging && droppable(sec) && s.Shelves) {
			continue
		}
		y += gtx.Dp(4)
		if len(out) > 0 {
			y += gtx.Dp(6)
		}
		h := gtx.Dp(sectionH)
		if len(ids) == 0 {
			h += gtx.Dp(emptyRowH)
		}
		out = append(out, elem{kind: 'h', id: strconv.Itoa(int(sec)), top: y, head: y, bot: y + h})
		y += h
		for i, id := range ids {
			if i > 0 {
				y += gtx.Dp(2)
			}
			h := rowH + statusHeight(gtx, v.th, v.status[id])
			out = append(out, elem{kind: 's', id: id, top: y, bot: y + h})
			y = s.placeSubs(gtx, v, id, "", 0, y+h, &out)
		}
	}
	// The queued tasks follow the rows, flush, the loose ones first.
	y += gtx.Dp(4)
	groups := []string{""}
	for _, p := range v.st.Projects {
		groups = append(groups, p.ID)
	}
	for _, g := range groups {
		n := len(s.queue)
		y = s.placeQueue(gtx, v, g, len(out)-1, y)
		for i := n; i < len(s.queue); i++ {
			s.queue[i].x = 0
		}
	}
	return out, y + gtx.Dp(6)
}

// headerSection is the section of a header elem.
func headerSection(e elem) section {
	n, _ := strconv.Atoi(e.id)
	return section(n)
}

// sectionAt is the section whose header is at or above content y in elems,
// the first one above them all.
func sectionAt(elems []elem, y int) section {
	sec, found := secActive, false
	for _, e := range elems {
		if e.kind != 'h' {
			continue
		}
		if !found || e.top <= y {
			sec, found = headerSection(e), true
		}
	}
	return sec
}

// sectionBand is the content span of sec's header and rows in elems.
func sectionBand(elems []elem, sec section) (top, bot int, ok bool) {
	for i, e := range elems {
		if e.kind != 'h' || headerSection(e) != sec {
			continue
		}
		top, bot = e.top, e.bot
		for _, r := range elems[i+1:] {
			if r.kind == 'h' {
				break
			}
			bot = r.bot
		}
		return top, bot, true
	}
	return 0, 0, false
}

// dropInbox sends the moves for a drop into d.sec, each tab by the verb
// from its own section, or reports false when it moves nothing.
func (s *Sidebar) dropInbox(v *view, d drop) bool {
	verbs := map[string]verb{}
	var ids []string
	what := ""
	for _, id := range s.drag.ids {
		vb := dropVerb(v.secOf[id], d.sec)
		if vb == verbNone || !s.Shelves && vb != verbPin && vb != verbUnpin {
			continue
		}
		verbs[id], ids = vb, append(ids, id)
		if what == "" || what == verbText[vb] {
			what = verbText[vb]
		} else {
			what = "Moved"
		}
	}
	if len(ids) == 0 {
		return false
	}
	s.reshelve(v, ids, what, func(id string, sh shelf) shelf { return sh.apply(verbs[id]) })
	return true
}

// undoNote is the notice after a move in the inbox: what happened, and
// the events that put it back.
type undoNote struct {
	text   string
	events []Event
	at     time.Time
}

// reshelve moves each tab of ids to change of its shelf, and offers to
// undo it in a notice that says what. Pinning moves a tab, so the undo
// puts it back where it was too.
func (s *Sidebar) reshelve(v *view, ids []string, what string, change func(id string, sh shelf) shelf) {
	var back, places []Event
	for _, id := range ids {
		w := v.st.Workspace(id)
		if w == nil {
			continue
		}
		from := shelfOf(*w)
		to := change(id, from)
		s.events = append(s.events, shelfEvents(*w, to)...)
		after := *w
		after.Pinned, after.Settled, after.SnoozedUntil = to.pinned, time.Time{}, to.until
		if to.settled {
			after.Settled = s.now
		}
		back = append(back, shelfEvents(after, from)...)
		if from.pinned != to.pinned && w.Parent == "" {
			places = append(places, placeOf(v, *w))
		}
	}
	back = append(back, places...)
	if len(back) == 0 {
		return
	}
	text := what + " " + fmt.Sprint(len(ids)) + " tabs"
	if len(ids) == 1 {
		text = what + " " + Title(*v.st.Workspace(ids[0]))
	}
	s.undo = &undoNote{text: text, events: back, at: s.now}
}

// placeOf is the move that puts tab w back where it is now: before the
// next tab of its group, or before the next top-level item.
func placeOf(v *view, w model.Workspace) MoveSession {
	next := func(ids []string) string {
		if i := slices.Index(ids, w.ID); i >= 0 && i+1 < len(ids) {
			return ids[i+1]
		}
		return ""
	}
	if v.groups[w.ProjectID] {
		var ids []string
		for _, x := range v.st.Workspaces {
			if x.ProjectID == w.ProjectID {
				ids = append(ids, x.ID)
			}
		}
		return MoveSession{WorkspaceID: w.ID, GroupID: w.ProjectID, Before: next(ids)}
	}
	return MoveSession{WorkspaceID: w.ID, Before: next(v.st.TopOrder(v.session))}
}

// sectionHeader draws sec's header in gtx's width: its name in small
// capitals and its count, then, for an empty section, what it is for.
func (s *Sidebar) sectionHeader(gtx layout.Context, v *view, sec section, n int) {
	th := v.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(sectionH)
	gtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(24), h))
	off := op.Offset(image.Pt(gtx.Dp(12), 0)).Push(gtx.Ops)
	items := []item{{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Muted, upper(sectionTitle[sec]))
	}}}
	if n > 0 {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.TextQuiet, strconv.Itoa(n))
		}})
	}
	hrow(gtx, h, gtx.Dp(6), items...)
	off.Pop()
	if n > 0 || sectionEmpty[sec] == "" {
		return
	}
	gtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(24), gtx.Dp(emptyRowH)))
	off = op.Offset(image.Pt(gtx.Dp(12), h)).Push(gtx.Ops)
	hrow(gtx, gtx.Dp(emptyRowH), 0, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.TextQuiet, sectionEmpty[sec])
	}})
	off.Pop()
}

// upper is s in capitals; section titles are ASCII.
func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// receded reports whether tab id's inbox row dims this frame.
func (s *Sidebar) receded(v *view, id string) bool {
	return v.inbox && s.menuWS != id && !s.row(id).hovered() &&
		recedes(v.need[id], v.unseen[id] != nil, id == v.active, s.selected[id])
}

// groupTag is an inbox row's group: its icon and name, at most 96dp.
func (s *Sidebar) groupTag(gtx layout.Context, v *view, p model.Project, col color.NRGBA) layout.Dimensions {
	th := v.th
	g := gtx
	g.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(96))
	return hrowFit(g, gtx.Constraints.Max.Y, gtx.Dp(4),
		item{w: func(gtx layout.Context) layout.Dimensions {
			if f := s.groupFavicon(v, p); f != nil {
				return f.draw(gtx, gtx.Dp(11))
			}
			return drawIcon(gtx, projectIcon(p.Icon), gtx.Dp(11), th.ProjectColor(p.Color), 0)
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), col, p.Name)
		}},
	)
}

// undoBar draws the undo notice at the bottom of the list, w wide, and
// reports whether it shows.
func (s *Sidebar) undoBar(gtx layout.Context, th *theme.Theme, w, h int) bool {
	u := s.undo
	if u == nil {
		return false
	}
	if gtx.Now.Sub(u.at) >= undoFor {
		s.undo = nil
		return false
	}
	gtx.Execute(op.InvalidateCmd{At: u.at.Add(undoFor)})
	pad := gtx.Dp(8)
	bh := gtx.Dp(36)
	rect := image.Rect(pad, h-pad-bh, w-pad, h-pad)
	in := anim.At(gtx, u.at, anim.Menu)
	if in < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
	defer op.Offset(image.Pt(0, int(float32(gtx.Dp(8))*(1-in)))).Push(gtx.Ops).Pop()
	defer paint.PushOpacity(gtx.Ops, in).Pop()
	kit.Shadow(gtx, rect, gtx.Dp(8), kit.Raised)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect, gtx.Dp(8)).Op(gtx.Ops))
	bo := op.Offset(rect.Min).Push(gtx.Ops)
	s.blockClicks(gtx, rect.Size())
	bo.Pop()
	g := gtx
	g.Constraints = layout.Exact(image.Pt(rect.Dx()-gtx.Dp(16), bh))
	defer op.Offset(rect.Min.Add(image.Pt(gtx.Dp(12), 0))).Push(gtx.Ops).Pop()
	hrow(g, bh, gtx.Dp(8),
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, u.text)
		}},
		item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return pillButton(&s.undoBtn, th, th.SurfaceSecondary, gtx.Dp(24), "Undo", th.Primary, false)(gtx)
		}},
	)
	return true
}
