package app

import (
	"cmp"
	"context"
	"image"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/skillshare"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

// OpenSkillshare opens the skillshare popup once the window shows a tab,
// for pwdemo-app's -skillshare: "overview", "library", or a row's name to
// preview. "" for none.
var OpenSkillshare string

// How often the reads run: while the popup shows, and for the sidebar
// button's badge while it does not.
const (
	ssOpenEvery   = 20 * time.Second
	ssClosedEvery = 2 * time.Minute
	ssNoteFor     = 6 * time.Second
)

// skillshareUI is the skillshare popup: Overview and Library over the
// store's reads, a row's preview, and the install steps. The reads and the
// commands run off the UI goroutine; see internal/skillshare.
type skillshareUI struct {
	store   skillshare.Store
	dir     string    // the folder the reads are for: the focused pane's
	nextAt  time.Time // the next refresh
	library bool      // Library shows, else Overview
	kind    skillshare.Kind
	scope   string // "all", skillshare.Project or skillshare.Global
	sel     string // the previewed row's key, "" for none
	confirm bool   // Uninstall asked once
	shown   skillshare.Preview
	doc     mdview.Doc
	inst    *ssInstall // the install steps while they show
	ask     string     // a path Ask was clicked for, sent after the clicks

	list   widget.List
	clicks map[string]*widget.Clickable
	acts   map[string]func() // what each click does, as last drawn

	mu   sync.Mutex
	busy string             // what the running command does, "" for none
	note skillshare.Outcome // the last command's outcome
	at   time.Time          // when note came
}

// ssInstall is the install steps: Source, Choose, Audit, Install.
type ssInstall struct {
	url    widget.Editor
	scope  string
	step   int // 0 source, 1 choose, 2 audit, 3 install, 4 done
	plan   skillshare.Plan
	picked map[string]bool
	audit  *skillshare.AuditResult // a blocked audit, shown before anything is written
	done   []string
	err    string
	work   bool // a step runs
}

// installed reports whether the skillshare CLI is on PATH.
func ssInstalled() bool { return skillshare.Default.Installed() }

// toggleSkillshare opens the popup, or closes it.
func (u *ui) toggleSkillshare(st *model.State) {
	if u.modal.kind == modalSkillshare {
		u.closeSkillshare()
		return
	}
	if !ssInstalled() || u.modal.kind != modalNone {
		return
	}
	u.modal.open(modalSkillshare, "")
	s := &u.ss
	s.sel, s.inst, s.confirm = "", nil, false
	if s.scope == "" {
		s.scope = "all"
	}
	if s.kind == "" {
		s.kind = skillshare.Skills
	}
	s.follow(u.ssDir(st), true, u.invalidateFn())
}

func (u *ui) closeSkillshare() {
	if in := u.ss.inst; in != nil {
		skillshare.Drop(in.plan.Dir)
	}
	u.ss.inst = nil
	u.modal.close()
}

func (u *ui) invalidateFn() func() {
	if u.invalidate == nil {
		return func() {}
	}
	return u.invalidate
}

// ssDir is the focused pane's folder, else the shown tab's.
func (u *ui) ssDir(st *model.State) string {
	if p := findPane(st, u.nav.focused()); p != nil && p.Cwd != "" {
		return p.Cwd
	}
	if ws := findWorkspace(st, u.nav.workspace); ws != nil {
		return st.LivePath(*ws)
	}
	return ""
}

// follow reads dir's scopes again when dir changed, when now is set, or
// when the refresh is due.
func (s *skillshareUI) follow(dir string, now bool, changed func()) {
	if !now && dir == s.dir && time.Now().Before(s.nextAt) {
		return
	}
	project := skillshare.FindProject(dir)
	if s.dir != dir && project == "" && s.scope == skillshare.Project {
		s.scope = "all"
	}
	s.dir = dir
	s.nextAt = time.Now().Add(ssClosedEvery)
	s.store.Refresh(skillshare.Default, project, false, changed)
}

// tickSkillshare keeps the reads fresh: every ssOpenEvery while the popup
// shows, every ssClosedEvery for the badge, and when the focused folder
// changes while it shows. It reports the badge's count.
func (u *ui) tickSkillshare(gtx gl.Context, st *model.State) int {
	if !ssInstalled() {
		return 0
	}
	s := &u.ss
	open := u.modal.kind == modalSkillshare
	dir := s.dir
	if open || s.dir == "" {
		dir = u.ssDir(st)
	}
	s.follow(dir, false, u.invalidateFn())
	if open && time.Until(s.nextAt) > ssOpenEvery {
		s.nextAt = time.Now().Add(ssOpenEvery)
	}
	gtx.Execute(op.InvalidateCmd{At: s.nextAt})
	v := s.store.View()
	n := 0
	for _, it := range v.Items {
		if it.WaitsForSync() || it.State == skillshare.Update {
			n++
		}
	}
	return n
}

// ssBusy is what the running command does, "" when none runs.
func (s *skillshareUI) running() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// run runs one command off the UI goroutine, one at a time; the status
// line shows its steps and then its outcome, and the reads run again.
func (u *ui) ssRun(first string, f func(ctx context.Context, step func(string)) skillshare.Outcome) {
	s := &u.ss
	s.mu.Lock()
	if s.busy != "" {
		s.mu.Unlock()
		return
	}
	s.busy = first
	s.mu.Unlock()
	inv := u.invalidateFn()
	project := skillshare.FindProject(s.dir)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		out := f(ctx, func(t string) {
			s.mu.Lock()
			s.busy = t
			s.mu.Unlock()
			inv()
		})
		s.mu.Lock()
		s.busy, s.note, s.at = "", out, time.Now()
		s.mu.Unlock()
		s.store.Refresh(skillshare.Default, project, true, inv)
		inv()
	}()
}

// ssSync syncs the scopes the switch shows that wait.
func (u *ui) ssSync(v skillshare.View) {
	scopes := skillshare.SyncScopes(v.Items, v.Reads, u.ss.scope)
	plugins := slices.ContainsFunc(v.Items, func(it skillshare.Item) bool {
		return it.Kind == skillshare.Plugins && it.WaitsForSync() && skillshare.Shown(u.ss.scope, it.Scope)
	})
	project := v.Project
	u.ssRun("Syncing…", func(ctx context.Context, step func(string)) skillshare.Outcome {
		return skillshare.Default.Sync(ctx, scopes, plugins, project, step)
	})
}

func (u *ui) ssUpdate(items []skillshare.Item, all bool, project string) {
	u.ssRun("Updating…", func(ctx context.Context, step func(string)) skillshare.Outcome {
		return skillshare.Default.UpdateItems(ctx, items, all, project, step)
	})
}

// ssAgent is the agent Ask sends to: the focused pane's, else the shown
// tab's first.
func (u *ui) ssAgent(st *model.State) *model.Pane {
	if p := findPane(st, u.nav.focused()); p != nil && model.IsAgent(p.Provider) {
		return p
	}
	return agentPane(st, findWorkspace(st, u.nav.workspace))
}

// ssAsk sends "audit this skill: <path>" to the agent, as Send failures
// queues its prompt, and closes the popup.
func (u *ui) ssAsk(gtx gl.Context, st *model.State, path string) {
	p := u.ssAgent(st)
	if p == nil {
		return
	}
	u.closeSkillshare()
	area := image.Rectangle{Max: gtx.Constraints.Max}
	if st.DraftHeld[p.ID] || slices.ContainsFunc(u.review.queue, func(d delivery) bool { return d.pane == p.ID && d.send }) {
		u.showNotice(gtx, "A message already waits for "+agentName(p)+"; this one was dropped", area)
		return
	}
	u.review.enqueue(p.ID, skillshare.AuditPrompt(path), nil).send = true
	msg := "Queued until " + agentName(p) + " waits for input"
	if agentReady(st, p.ID) {
		msg = sentNote(st, p)
	}
	u.showNotice(gtx, msg, area)
	u.deliver(st, gtx.Now)
}

// ssSelect previews the row with key, reading its files now: they are
// local and capped at 60 kB.
func (s *skillshareUI) open(it skillshare.Item, r *skillshare.Read) {
	s.sel, s.confirm = it.Key, false
	s.shown = skillshare.Default.LoadPreview(it, r)
	var blocks []mdview.Block
	if s.shown.Body != "" {
		blocks = mdview.Parse([]byte(s.shown.Body))
	}
	s.doc.Set(blocks)
	s.list.Position = gl.Position{}
}

// startInstall shows the install steps.
func (s *skillshareUI) startInstall(project string) {
	in := &ssInstall{scope: skillshare.Global, picked: map[string]bool{}}
	if project != "" && s.scope != skillshare.Global {
		in.scope = skillshare.Project
	}
	in.url.SingleLine, in.url.Submit = true, true
	s.inst, s.sel = in, ""
}

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/main.js
// (the install steps: source, choose, audit, install anyway).

// discover clones the source and asks the CLI for its plan.
func (u *ui) ssDiscover(project string) {
	in := u.ss.inst
	source := strings.TrimSpace(in.url.Text())
	if source == "" || in.work {
		return
	}
	in.work, in.err = true, ""
	inv := u.invalidateFn()
	scope := in.scope
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		p := skillshare.Default.Discover(ctx, source, scope, ssCwd(scope, project))
		u.ss.mu.Lock()
		defer u.ss.mu.Unlock()
		if u.ss.inst != in {
			skillshare.Drop(p.Dir)
			return
		}
		in.work, in.plan = false, p
		switch {
		case p.Err != "":
			in.err = p.Err
		case len(p.Choices) == 1:
			in.picked[p.Choices[0].Name] = true
			in.step = 2
			u.ssAuditLocked(in, project, inv)
		default:
			in.step = 1
		}
		inv()
	}()
}

func ssCwd(scope, project string) string {
	if scope == skillshare.Project {
		return project
	}
	return ""
}

// ssAuditLocked audits the picked skills, then installs them when the
// audit lets them through. Called with u.ss.mu held.
func (u *ui) ssAuditLocked(in *ssInstall, project string, inv func()) {
	in.work, in.step = true, 2
	var picks []skillshare.Choice
	for _, c := range in.plan.Choices {
		if in.picked[c.Name] {
			picks = append(picks, c)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		a := skillshare.Default.Audit(ctx, in.plan.Dir, in.scope, ssCwd(in.scope, project), picks)
		u.ss.mu.Lock()
		if u.ss.inst != in {
			u.ss.mu.Unlock()
			return
		}
		if len(a.Blocked) > 0 {
			in.audit, in.work = &a, false
			u.ss.mu.Unlock()
			inv()
			return
		}
		u.ss.mu.Unlock()
		u.ssInstallNow(in, project, false, inv)
	}()
}

// ssInstallNow installs the picked skills; force goes past a blocked
// audit, after the user saw its findings.
func (u *ui) ssInstallNow(in *ssInstall, project string, force bool, inv func()) {
	u.ss.mu.Lock()
	in.work, in.step, in.audit = true, 3, nil
	var names []string
	for _, c := range in.plan.Choices {
		if in.picked[c.Name] {
			names = append(names, c.Name)
		}
	}
	if len(in.plan.Choices) == 1 {
		names = nil // the whole source: it names one skill
	}
	source := strings.TrimSpace(in.url.Text())
	u.ss.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		done, errText := skillshare.Default.Install(ctx, source, in.scope, ssCwd(in.scope, project), names, force)
		u.ss.mu.Lock()
		skillshare.Drop(in.plan.Dir)
		in.plan.Dir, in.work = "", false
		if errText != "" {
			in.err = errText
		} else {
			in.done, in.step = done, 4
		}
		u.ss.mu.Unlock()
		u.ss.store.Refresh(skillshare.Default, project, false, inv)
		inv()
	}()
}

// click is id's clickable, made on first use.
func (s *skillshareUI) click(id string) *widget.Clickable {
	if s.clicks == nil {
		s.clicks = map[string]*widget.Clickable{}
	}
	c := s.clicks[id]
	if c == nil {
		c = new(widget.Clickable)
		s.clicks[id] = c
	}
	return c
}

// layoutSkillshare handles the popup's input and draws it. Escape leaves
// the install steps or the preview, else closes the popup, as does a
// press outside the card.
func (u *ui) layoutSkillshare(gtx gl.Context, st *model.State) {
	m, s := &u.modal, &u.ss
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &m.body, Kinds: pointer.Press}); !ok {
			break
		}
	}
	// FocusFilter: the router focuses only a tag that declares it.
	filters := []event.Filter{pointer.Filter{Target: &m.backdrop, Kinds: pointer.Press}, key.FocusFilter{Target: &m.backdrop},
		key.Filter{Focus: &m.backdrop, Name: key.NameEscape}}
	if s.inst != nil {
		filters = append(filters, key.Filter{Focus: &s.inst.url, Name: key.NameEscape})
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
		case pointer.Event:
			u.closeSkillshare()
		case key.Event:
			if e.State != key.Press {
				break
			}
			switch {
			case s.inst != nil:
				s.mu.Lock()
				skillshare.Drop(s.inst.plan.Dir)
				s.inst = nil
				s.mu.Unlock()
				gtx.Execute(key.FocusCmd{Tag: &m.backdrop})
			case s.sel != "":
				s.sel = ""
			default:
				u.closeSkillshare()
			}
		}
	}
	if m.kind != modalSkillshare {
		return
	}
	v := s.store.View()
	if s.inst != nil {
		for {
			ev, ok := s.inst.url.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				s.mu.Lock()
				u.ssDiscover(v.Project)
				s.mu.Unlock()
			}
		}
	}
	for id, c := range s.clicks {
		for c.Clicked(gtx) {
			if a := s.acts[id]; a != nil {
				a()
			}
		}
	}
	if path := s.ask; path != "" {
		s.ask = ""
		u.ssAsk(gtx, st, path)
	}
	if m.kind != modalSkillshare {
		return
	}
	if v.Project == "" && s.scope == skillshare.Project {
		s.scope = "all"
	}
	s.acts = map[string]func(){}
	u.drawSkillshare(gtx, st, v)
}

// demoSkillshare opens the popup on OpenSkillshare's view; a row's
// preview waits for the reads to name it.
func (u *ui) demoSkillshare(st *model.State) {
	if u.modal.kind != modalSkillshare {
		u.toggleSkillshare(st)
	}
	s := &u.ss
	switch OpenSkillshare {
	case "overview":
	case "library":
		s.library = true
	default:
		v := s.store.View()
		it, ok := ssPick(v.Items, OpenSkillshare)
		if !ok || s.store.Running() {
			return
		}
		s.library, s.kind = true, it.Kind
		s.open(it, ssReadOf(v, it))
	}
	OpenSkillshare = ""
}

// ssPick is the popup's first row named name, for pwdemo-app.
func ssPick(items []skillshare.Item, name string) (skillshare.Item, bool) {
	i := slices.IndexFunc(items, func(it skillshare.Item) bool { return it.Name == name && !it.Notice })
	if i < 0 {
		return skillshare.Item{}, false
	}
	return items[i], true
}

// ssReadOf is the read of it's scope.
func ssReadOf(v skillshare.View, it skillshare.Item) *skillshare.Read {
	return v.Reads[cmp.Or(it.Scope, skillshare.Global)]
}
