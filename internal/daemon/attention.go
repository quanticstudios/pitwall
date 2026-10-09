package daemon

import (
	"io"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// noticeInterval is the least time between two OSC notifications a pane
// shows; one that comes sooner waits, and only the latest waiting one is
// shown. Tests shorten it.
var noticeInterval = time.Second

// attention is what the daemon keeps per pane for Activity.Unseen and OSC
// notifications. Guarded by d.mu.
type attention struct {
	seen    time.Time        // when a GUI last showed the pane focused
	notice  *model.Activity  // the pane's OSC notification, until seen or replaced
	last    time.Time        // when the last notification was shown
	pending *vt.Notification // the latest one noticeInterval holds back
}

// attnOf returns the pane's record, making it. Callers hold d.mu.
func (d *Daemon) attnOf(id string) *attention {
	if d.attn == nil {
		d.attn = map[string]*attention{}
	}
	a := d.attn[id]
	if a == nil {
		a = &attention{}
		d.attn[id] = a
	}
	return a
}

// bellInterval is the least time between two bells of a pane that reach
// the daemon; the ones between are dropped. Tests shorten it.
var bellInterval = time.Second

// clipKeep is how long states carry an OSC 52 write's text, for GUIs to
// pick it up; later states carry only its Seq.
const clipKeep = 5 * time.Second

// notifyingVT wraps d.o.NewVT so the emulator of pane id reports OSC
// notifications, bells and OSC 52 clipboard writes to the daemon.
func (d *Daemon) notifyingVT(id string) vt.NewFunc {
	newVT := d.o.NewVT
	if newVT == nil {
		return nil
	}
	return func(cols, rows int, reply io.Writer) vt.Emulator {
		e := newVT(cols, rows, reply)
		// why: the emulator calls these under the pane's lock, and d.mu
		// is taken before pane locks elsewhere.
		if s, ok := e.(interface{ SetNotifyFunc(func(vt.Notification)) }); ok {
			s.SetNotifyFunc(func(n vt.Notification) { go d.notice(id, n) })
		}
		if s, ok := e.(interface{ SetBellFunc(func()) }); ok {
			var last time.Time // guarded by the pane's lock
			s.SetBellFunc(func() {
				if now := time.Now(); now.Sub(last) >= bellInterval {
					last = now
					go d.bell(id)
				}
			})
		}
		if s, ok := e.(interface{ SetClipboardFunc(func(string)) }); ok {
			s.SetClipboardFunc(func(text string) {
				seq := d.clipSeq.Add(1) // the order they arrived in, whichever goroutine runs first
				go d.setClipboard(seq, text)
			})
		}
		return e
	}
}

// bell raises pane id's attention for a BEL, as an OSC notification
// does, unless the config turns bells off or a GUI shows the pane focused.
// A pane whose agent already needs you keeps its activity: the hooks say
// more than a bell.
func (d *Daemon) bell(id string) {
	if d.o.Bell != nil && !d.o.Bell() {
		return
	}
	d.mu.Lock()
	quiet := d.shownFocused(id)
	if i := d.activityIndex(id); i >= 0 && model.NeedsYou(d.st.Activities[i].State) {
		quiet = true
	}
	d.mu.Unlock()
	if !quiet {
		d.notice(id, vt.Notification{Body: "Bell"})
	}
}

// shownFocused reports whether a GUI shows pane id focused in a focused
// window. Callers hold d.mu.
func (d *Daemon) shownFocused(id string) bool {
	for c := range d.clients {
		if c.focus == id {
			return true
		}
	}
	return false
}

// setClipboard makes text, the OSC 52 write numbered seq, the clipboard
// GUIs write, unless a later write got there first.
func (d *Daemon) setClipboard(seq uint64, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || seq <= d.clip.Seq {
		return
	}
	d.clip, d.clipAt = model.Clipboard{Seq: seq, Text: text}, time.Now()
	d.changed()
}

// bellSetting reads [terminal] bell from the config at path, again only
// when the file changed.
func bellSetting(path string) func() bool {
	return setting(path, func(s config.Settings) bool { return s.Bell })
}

// setting reads one switch, pick, from the config at path, again only when
// the file changed.
func setting(path string, pick func(config.Settings) bool) func() bool {
	var mu sync.Mutex
	var stamp string
	on := true
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if st := fileStamp(path); st != stamp {
			stamp = st
			s, _ := config.LoadFile(path)
			on = pick(s)
		}
		return on
	}
}

// notice shows an OSC notification from pane id as an awaiting-input
// activity with the text as its Detail.
// ponytail: one goroutine per notification, so two in one PTY read may land
// in either order; a per-pane queue if that ever matters.
func (d *Daemon) notice(id string, n vt.Notification) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.panes[id] == nil {
		return
	}
	a := d.attnOf(id)
	if wait := time.Until(a.last.Add(noticeInterval)); wait > 0 {
		if a.pending == nil {
			time.AfterFunc(wait, func() { d.flushNotice(id) })
		}
		a.pending = &n
		return
	}
	d.showNotice(id, a, n)
}

func (d *Daemon) flushNotice(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.attn[id]
	if d.closing || a == nil || a.pending == nil {
		return
	}
	n := *a.pending
	a.pending = nil
	d.showNotice(id, a, n)
}

// showNotice records n as the pane's notification. Callers hold d.mu.
func (d *Daemon) showNotice(id string, a *attention, n vt.Notification) {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		return
	}
	p := d.st.Panes[i]
	prov := p.Provider
	if prov == "" {
		prov = model.ProviderTerminal
	}
	detail := n.Body
	switch {
	case n.Body == "":
		detail = n.Title
	case n.Title != "":
		detail = n.Title + ": " + n.Body
	}
	detail = decide.Redact(detail, d.dec.cur.Secrets...) // it shows in the sidebar and desktop notifications
	now := time.Now()
	a.last = now
	a.notice = &model.Activity{PaneID: id, WorkspaceID: p.WorkspaceID, Provider: prov,
		State: model.StateAwaitingInput, Detail: detail, UpdatedAt: now}
	if w := d.workspace(p.WorkspaceID); w != nil {
		w.UpdatedAt = now
	}
	d.changed()
}

// seePane marks pane id seen: a GUI shows it focused in a focused window.
func (d *Daemon) seePane(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.panes[id] == nil {
		return nil // closed while the GUI looked at it
	}
	a := d.attnOf(id)
	a.seen, a.notice = time.Now(), nil
	d.noteSeen(id, a.seen)
	d.changed()
	return nil
}

// pruneNotices drops each notification that a newer activity of its pane
// replaced. Callers hold d.mu.
func (d *Daemon) pruneNotices() {
	for id, a := range d.attn {
		if i := d.activityIndex(id); a.notice != nil && i >= 0 && d.st.Activities[i].UpdatedAt.After(a.notice.UpdatedAt) {
			a.notice = nil
		}
	}
}

// attended is st.Activities as clients see them: a pane's OSC notification
// in place of its activity, and Unseen set on each needs-you activity that
// changed since its pane was last seen. Callers hold d.mu.
func (d *Daemon) attended() []model.Activity {
	out := slices.Clone(d.st.Activities)
	for _, p := range d.st.Panes {
		a := d.attn[p.ID]
		if a == nil || a.notice == nil {
			continue
		}
		if i := slices.IndexFunc(out, func(x model.Activity) bool { return x.PaneID == p.ID }); i >= 0 {
			out[i] = *a.notice
		} else {
			out = append(out, *a.notice)
		}
	}
	for i := range out {
		var seen time.Time
		if a := d.attn[out[i].PaneID]; a != nil {
			seen = a.seen
		}
		out[i].Unseen = model.NeedsYou(out[i].State) && out[i].UpdatedAt.After(seen)
	}
	return out
}
