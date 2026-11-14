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
	"github.com/quanticstudios/pitwall/internal/proto"
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
	// machines are the reads and cards of each machine, by host name, ""
	// for this one; to is the machine the popup shows.
	machines map[string]*ssMachine
	to       ssTarget
	// picked is the machine chosen in the header or on Machines, kept
	// while the focus stays on a tab of pickedFrom.
	picked     *string
	pickedFrom string
	menu       bool // the machine picker is open

	library  bool // Library shows, else Overview or Machines
	machView bool // Machines shows
	kind     skillshare.Kind
	scope    string // "all", skillshare.Project or skillshare.Global
	sel      string // the previewed row's key, "" for none
	confirm  bool   // Uninstall asked once
	shown    skillshare.Preview
	doc      mdview.Doc
	inst     *ssInstall // the install steps while they show
	ask      string     // a path Ask was clicked for, sent after the clicks

	list   widget.List
	clicks map[string]*widget.Clickable
	acts   map[string]func() // what each click does, as last drawn

	mu        sync.Mutex
	busy      string             // what the running command does, "" for none
	note      skillshare.Outcome // the last command's outcome
	at        time.Time          // when note came
	loaded    *ssLoaded          // a preview read off the UI goroutine, not yet shown
	previewID uint64             // invalidates replies from a previous preview
}

// ssTarget is a machine the popup's work goes to: its host name, "" for
// this machine, and the focused pane, which routes the work in a window
// of several machines; "" for a machine picked by name.
type ssTarget struct{ host, pane string }

// ssMachine is one machine's reads and its Machines card.
type ssMachine struct {
	store  skillshare.Store
	dir    string    // the folder the reads are for
	nextAt time.Time // the next refresh
	// Guarded by skillshareUI.mu.
	card    *skillshare.Machine
	reading bool // a card read is out
	cardAt  time.Time
	follow  uint64 // identity of the pending project lookup
}

// ssLoaded is a row's preview, read for key.
type ssLoaded struct {
	key    string
	id     uint64
	p      skillshare.Preview
	blocks []mdview.Block
}

// Skillsharer is optionally implemented by a Backend: the skillshare
// popup's work runs on the daemon of the machine it is for.
type Skillsharer interface {
	// Skillshare sends m to the daemon of m.Pane's machine, or of host
	// when m.Pane is "", and waits for its reply; step gets its steps.
	Skillshare(ctx context.Context, host string, m proto.Skillshare, step func(string)) (skillshare.Reply, error)
	// SkillshareOn reports whether host's daemon has skillshare on its
	// PATH, and old when that daemon is too old to say or to run it.
	SkillshareOn(host string) (installed, old bool)
}

// ssInstall is the install steps: Source, Choose, Audit, Install.
type ssInstall struct {
	url     widget.Editor
	scope   string
	step    int // 0 source, 1 choose, 2 audit, 3 install, 4 done
	plan    skillshare.Plan
	picked  map[string]bool
	audit   *skillshare.AuditResult // a blocked audit, shown before anything is written
	done    []string
	err     string
	work    bool   // a step runs
	source  string // source and project captured for this plan
	project string
	do      skillshare.Do // the machine it installs on, fixed when it opens
	cancel  context.CancelFunc
}

// ssShown is whether skillshare shows for the focused tab, which the
// palette reads; the window's goroutine sets it every frame.
var ssShown bool

// ssOn reports whether skillshare is on host's machine, and old when its
// daemon predates the popup running there. A backend that runs no daemon
// (the demo's, the tests') uses skillshare.Default on this machine.
func (u *ui) ssOn(host string) (installed, old bool) {
	if sk, ok := u.b.(Skillsharer); ok {
		return sk.SkillshareOn(host)
	}
	return skillshare.Default.Installed(), false
}

// ssVisible is whether the button, palette entry and key show: skillshare
// is on the focused tab's machine, or its daemon is too old to say, so
// the popup can explain that it needs an update.
func (u *ui) ssVisible(st *model.State) bool {
	host := u.ssFocusHost(st)
	on, old := u.ssOn(host)
	return on || old
}

// ssFocusHost is the focused tab's machine.
func (u *ui) ssFocusHost(st *model.State) string {
	if p := u.nav.focused(); p != "" && findPane(st, p) != nil {
		return u.hostOf(p)
	}
	if u.nav.workspace != "" {
		return u.hostOf(u.nav.workspace)
	}
	return curHost()
}

// ssTargetOf is the machine the popup shows: the focused tab's, unless
// one was picked while the focus stays on a tab of the same machine.
func (u *ui) ssTargetOf(st *model.State) ssTarget {
	s := &u.ss
	host := u.ssFocusHost(st)
	if s.picked != nil && host == s.pickedFrom {
		return ssTarget{host: *s.picked}
	}
	s.picked = nil
	pane := u.nav.focused()
	if findPane(st, pane) == nil {
		pane = ""
	}
	return ssTarget{host: host, pane: pane}
}

// ssPickHost points Overview and Library at host until the focus moves to
// a tab on another machine.
func (u *ui) ssPickHost(st *model.State, host string) {
	s := &u.ss
	s.picked, s.pickedFrom, s.menu = &host, u.ssFocusHost(st), false
	s.sel, s.confirm, s.machView = "", false, false
	s.dismissInstall()
	u.ssFollow(st, true, true)
}

// ssOld is what the popup says for a machine whose daemon predates it.
func ssOld(host string) string {
	if host == "" {
		return "This computer's pitwall daemon is older than this window. Restart it to use skillshare."
	}
	return host + " runs an older pitwall that cannot run skillshare there. Update pitwall on " + host + "."
}

// ssDo is the Do for t's machine: its daemon through the backend, or
// skillshare.Default here for a backend that runs no daemon. A daemon too
// old for it fails every call with ssOld; nothing falls back to this
// machine.
func (u *ui) ssDo(t ssTarget) skillshare.Do {
	sk, ok := u.b.(Skillsharer)
	if !ok {
		return skillshare.Default.Serve
	}
	return func(ctx context.Context, c skillshare.Call, step func(string)) skillshare.Reply {
		if _, old := sk.SkillshareOn(t.host); old {
			return skillshare.Failed(ssOld(t.host))
		}
		pane := t.pane
		if c.Op == skillshare.OpDrop {
			pane = "" // cleanup stays on the captured host after its pane closes
		}
		r, err := sk.Skillshare(ctx, t.host, proto.Skillshare{Pane: pane, Call: c}, step)
		if err != nil {
			return skillshare.Failed(err.Error())
		}
		return r
	}
}

// machine is host's reads, made on first use.
func (s *skillshareUI) machine(host string) *ssMachine {
	if s.machines == nil {
		s.machines = map[string]*ssMachine{}
	}
	m := s.machines[host]
	if m == nil {
		m = &ssMachine{}
		s.machines[host] = m
	}
	return m
}

// cur is the shown machine's reads.
func (s *skillshareUI) cur() *ssMachine { return s.machine(s.to.host) }

// toggleSkillshare opens the popup, or closes it.
func (u *ui) toggleSkillshare(st *model.State) {
	if u.modal.kind == modalSkillshare {
		u.closeSkillshare()
		return
	}
	if !u.ssVisible(st) || u.modal.kind != modalNone {
		return
	}
	u.modal.open(modalSkillshare, "")
	s := &u.ss
	s.dismissInstall()
	s.sel, s.confirm, s.menu = "", false, false
	if s.scope == "" {
		s.scope = "all"
	}
	if s.kind == "" {
		s.kind = skillshare.Skills
	}
	u.ssFollow(st, true, true)
}

func (u *ui) closeSkillshare() {
	u.ss.dismissInstall()
	u.modal.close()
}

// dismissInstall invalidates pending transitions before cancelling their work.
func (s *skillshareUI) dismissInstall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in := s.inst; in != nil {
		if in.cancel != nil {
			in.cancel()
		}
		// A running worker owns cleanup until it has stopped using the clone.
		if !in.work {
			in.drop(in.plan.Dir)
		}
	}
	s.inst = nil
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

// ssFollow points the popup at the focused tab's machine, or the picked
// one, and reads its scopes again when the machine or folder changed, when
// now is set, or when the refresh is due. Closed, it keeps the folder it
// read last on the same machine, for the badge. A picked machine that is
// not the focused tab's has no folder there: it reads global only.
func (u *ui) ssFollow(st *model.State, now, open bool) {
	s := &u.ss
	t := u.ssTargetOf(st)
	m := s.machine(t.host)
	dir := m.dir
	switch {
	case open || now || dir == "" || t.host != s.to.host:
		dir = ""
		if t.pane != "" || t.host == u.ssFocusHost(st) {
			dir = u.ssDir(st)
		}
	default:
		t = s.to
	}
	moved := t != s.to
	s.to = t
	if moved || dir != m.dir {
		s.dismissInstall()
		s.sel, s.confirm, s.ask, s.menu = "", false, "", false
		s.shown = skillshare.Preview{}
		s.doc.Set(nil)
		s.acts = nil // clicks from the previous frame name the previous target
		s.list.Position = gl.Position{}
		s.mu.Lock()
		s.previewID++
		s.loaded = nil
		s.mu.Unlock()
	}
	if !now && !moved && dir == m.dir && time.Now().Before(m.nextAt) {
		return
	}
	m.dir = dir
	m.nextAt = time.Now().Add(ssClosedEvery)
	s.mu.Lock()
	m.follow++
	follow := m.follow
	s.mu.Unlock()
	do, changed := u.ssDo(t), u.invalidateFn()
	go func() {
		defer changed()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		project := ""
		if dir != "" {
			project = do(ctx, skillshare.Call{Op: skillshare.OpProject, Dir: dir}, nil).Project
		}
		s.mu.Lock()
		if m.follow == follow {
			m.store.Refresh(do, project, false, changed)
		}
		s.mu.Unlock()
	}()
}

// tickSkillshare keeps the reads fresh: every ssOpenEvery while the popup
// shows, every ssClosedEvery for the badge, and when the focused folder
// or machine changes while it shows. It reports the badge's count.
func (u *ui) tickSkillshare(gtx gl.Context, st *model.State) int {
	ssShown = u.ssVisible(st)
	open := u.modal.kind == modalSkillshare
	if !ssShown && !open {
		return 0
	}
	s := &u.ss
	u.ssFollow(st, false, open)
	m := s.cur()
	if open && time.Until(m.nextAt) > ssOpenEvery {
		m.nextAt = time.Now().Add(ssOpenEvery)
	}
	gtx.Execute(op.InvalidateCmd{At: m.nextAt})
	if open && s.machView {
		u.ssReadCards()
	}
	v := m.store.View()
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

// ssRun runs one command on the shown machine off the UI goroutine, one
// at a time; the status line shows its steps and then its outcome, and the
// reads run again.
func (u *ui) ssRun(first string, f func(ctx context.Context, do skillshare.Do, step func(string)) skillshare.Outcome) {
	u.ssRunOn(u.ss.to, first, f)
}

// ssRunOn is ssRun on t's machine.
func (u *ui) ssRunOn(t ssTarget, first string, f func(ctx context.Context, do skillshare.Do, step func(string)) skillshare.Outcome) {
	s := &u.ss
	s.mu.Lock()
	if s.busy != "" {
		s.mu.Unlock()
		return
	}
	s.busy = first
	s.mu.Unlock()
	inv := u.invalidateFn()
	m, do := s.machine(t.host), u.ssDo(t)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		out := f(ctx, do, func(t string) {
			s.mu.Lock()
			s.busy = t
			s.mu.Unlock()
			inv()
		})
		s.mu.Lock()
		s.busy, s.note, s.at = "", out, time.Now()
		m.cardAt = time.Time{} // read the card again too
		s.mu.Unlock()
		m.store.Refresh(do, m.store.View().Project, true, inv)
		inv()
	}()
}

// ssCall runs c on the shown machine as ssRun does, first saying first.
func (u *ui) ssCall(first string, c skillshare.Call) {
	u.ssRun(first, func(ctx context.Context, do skillshare.Do, step func(string)) skillshare.Outcome {
		return do(ctx, c, step).Outcome
	})
}

// ssSync syncs the scopes the switch shows that wait.
func (u *ui) ssSync(v skillshare.View) {
	scopes := skillshare.SyncScopes(v.Items, v.Reads, u.ss.scope)
	plugins := slices.ContainsFunc(v.Items, func(it skillshare.Item) bool {
		return it.Kind == skillshare.Plugins && it.WaitsForSync() && skillshare.Shown(u.ss.scope, it.Scope)
	})
	u.ssCall("Syncing…", skillshare.Call{Op: skillshare.OpSync, Scopes: scopes, Plugins: plugins, Dir: v.Project})
}

func (u *ui) ssUpdate(items []skillshare.Item, all bool, project string) {
	u.ssCall("Updating…", skillshare.Call{Op: skillshare.OpUpdate, Items: items, All: all, Dir: project})
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
	if p == nil || u.hostOf(p.ID) != u.ss.to.host {
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

// open previews it, reading its files on its machine off the UI
// goroutine: a skill's names, 40 at most, and 60 kB of its document.
func (u *ui) ssOpen(it skillshare.Item, r *skillshare.Read) {
	s := &u.ss
	s.sel, s.confirm = it.Key, false
	s.shown = skillshare.Preview{}
	s.doc.Set(nil)
	s.list.Position = gl.Position{}
	do, inv := u.ssDo(s.to), u.invalidateFn()
	s.mu.Lock()
	s.previewID++
	id := s.previewID
	s.mu.Unlock()
	paths := skillshare.PathsOf(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p := do(ctx, skillshare.Call{Op: skillshare.OpPreview, Item: it, Paths: paths}, nil).Preview
		l := &ssLoaded{key: it.Key, id: id, p: p}
		if p.Body != "" {
			l.blocks = mdview.Parse([]byte(p.Body))
		}
		s.mu.Lock()
		if s.previewID == id {
			s.loaded = l
		}
		s.mu.Unlock()
		inv()
	}()
}

// takePreview shows a preview that came for the row shown.
func (s *skillshareUI) takePreview() {
	s.mu.Lock()
	l := s.loaded
	s.loaded = nil
	s.mu.Unlock()
	if l != nil && l.key == s.sel && l.id == s.previewID {
		s.shown = l.p
		s.doc.Set(l.blocks)
	}
}

// startInstall shows the install steps, which run on the shown machine.
func (u *ui) startInstall(project string) {
	s := &u.ss
	s.dismissInstall()
	s.mu.Lock()
	defer s.mu.Unlock()
	in := &ssInstall{scope: skillshare.Global, picked: map[string]bool{}, do: u.ssDo(s.to)}
	if project != "" && s.scope != skillshare.Global {
		in.scope = skillshare.Project
	}
	in.url.SingleLine, in.url.Submit = true, true
	s.inst, s.sel = in, ""
}

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/main.js
// (the install steps: source, choose, audit, install anyway).

// drop removes an audit clone on the install's machine.
func (in *ssInstall) drop(dir string) {
	if dir == "" || in.do == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		in.do(ctx, skillshare.Call{Op: skillshare.OpDrop, Clone: dir}, nil)
	}()
}

// discover clones the source and asks the CLI for its plan.
func (u *ui) ssDiscover(project string) {
	in := u.ss.inst
	source := strings.TrimSpace(in.url.Text())
	if source == "" || in.work || in.step != 0 {
		return
	}
	in.work, in.err = true, ""
	in.source, in.project = source, project
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	in.cancel = cancel
	inv := u.invalidateFn()
	scope := in.scope
	go func() {
		defer inv()
		defer cancel()
		p := in.do(ctx, skillshare.Call{Op: skillshare.OpDiscover, Source: source, Scope: scope, Dir: project}, nil).Plan
		u.ss.mu.Lock()
		defer u.ss.mu.Unlock()
		if u.ss.inst != in {
			in.work = false
			in.drop(p.Dir)
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
	}()
}

// ssAuditLocked audits the picked skills, then installs them when the
// audit lets them through. Called with u.ss.mu held.
func (u *ui) ssAuditLocked(in *ssInstall, project string, inv func()) {
	if u.ss.inst != in || in.work || (in.step != 1 && in.step != 2) {
		return
	}
	in.work, in.step = true, 2
	dir, scope := in.plan.Dir, in.scope
	project = in.project
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	in.cancel = cancel
	var picks []skillshare.Choice
	for _, c := range in.plan.Choices {
		if in.picked[c.Name] {
			picks = append(picks, c)
		}
	}
	go func() {
		defer inv()
		defer cancel()
		a := in.do(ctx, skillshare.Call{Op: skillshare.OpAudit, Clone: dir, Scope: scope, Dir: project, Picks: picks}, nil).Audit
		u.ss.mu.Lock()
		if u.ss.inst != in {
			in.work = false
			u.ss.mu.Unlock()
			in.drop(dir)
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
	if u.ss.inst != in {
		dir := in.plan.Dir
		u.ss.mu.Unlock()
		in.drop(dir)
		return
	}
	if in.step != 2 || force && in.audit == nil {
		u.ss.mu.Unlock()
		return
	}
	in.work, in.step, in.audit = true, 3, nil
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	in.cancel = cancel
	var names []string
	for _, c := range in.plan.Choices {
		if in.picked[c.Name] {
			names = append(names, c.Name)
		}
	}
	if len(in.plan.Choices) == 1 {
		names = nil // the whole source: it names one skill
	}
	source, scope, dir := in.source, in.scope, in.plan.Dir
	project = in.project
	m := u.ss.cur()
	u.ss.mu.Unlock()
	go func() {
		defer cancel()
		r := in.do(ctx, skillshare.Call{Op: skillshare.OpInstall, Source: source, Scope: scope, Dir: project, Skills: names, Force: force}, nil)
		u.ss.mu.Lock()
		in.drop(dir)
		in.plan.Dir = ""
		if r.Err != "" {
			in.err = r.Err
		} else {
			in.done, in.step = r.Done, 4
		}
		u.ss.mu.Unlock()
		m.store.Refresh(in.do, m.store.View().Project, false, inv)
		u.ss.mu.Lock()
		in.work = false
		u.ss.mu.Unlock()
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
		key.Filter{Name: key.NameEscape}}
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
			case s.menu:
				s.menu = false
			case s.inst != nil:
				s.dismissInstall()
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
	s.takePreview()
	v := s.cur().store.View()
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
	v = s.cur().store.View() // a machine-picker click may have changed the target
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
		v := s.cur().store.View()
		it, ok := ssPick(v.Items, OpenSkillshare)
		if !ok || s.cur().store.Running() {
			return
		}
		s.library, s.kind = true, it.Kind
		u.ssOpen(it, ssReadOf(v, it))
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
