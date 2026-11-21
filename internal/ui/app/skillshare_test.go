package app

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type ssHostFake struct {
	hostFake
	do   skillshare.Do
	on   func(string) (bool, bool)
	sent func(proto.Skillshare)
}

func (b ssHostFake) HostOf(id string) string {
	st := b.State()
	if p := findPane(&st, id); p != nil {
		id = p.WorkspaceID
	}
	return b.hostFake.HostOf(id)
}

func (b ssHostFake) Skillshare(ctx context.Context, _ string, m proto.Skillshare, step func(string)) (skillshare.Reply, error) {
	if b.sent != nil {
		b.sent(m)
	}
	return b.do(ctx, m.Call, step), nil
}

func TestSkillshareCleanupAfterPaneClosed(t *testing.T) {
	b := ssHostFake{do: func(context.Context, skillshare.Call, func(string)) skillshare.Reply { return skillshare.Reply{} },
		sent: func(m proto.Skillshare) {
			if m.Pane != "" {
				t.Fatal("cleanup still depends on its removed pane")
			}
		}}
	u := &ui{b: b}
	u.ssDo(ssTarget{host: "box", pane: "removed"})(context.Background(), skillshare.Call{Op: skillshare.OpDrop, Clone: "/audit-clone"}, nil)
}

func (b ssHostFake) SkillshareOn(host string) (bool, bool) {
	if b.on != nil {
		return b.on(host)
	}
	return true, false
}

func TestSkillshareFollowsMissingCLI(t *testing.T) {
	b := ssHostFake{hostFake: hostFake{NewFakeBackend()}, do: skillshare.Demo().Serve,
		on: func(host string) (bool, bool) { return host == "", false }}
	st := b.State()
	u := &ui{b: b}
	u.nav.switchSession(&st, "s1")
	u.modal.open(modalSkillshare, "")
	u.ssFollow(&st, true, true)
	u.ss.sel, u.ss.confirm, u.ss.ask = "global:skills:check", true, "/old/path"
	u.ss.shown = skillshare.Preview{Body: "old machine"}
	u.ss.acts = map[string]func(){"p-toggle": func() { t.Fatal("stale action ran") }}
	canceled := false
	u.ss.inst = &ssInstall{work: true, cancel: func() { canceled = true }}
	u.nav.switchSession(&st, "s2")
	var ops op.Ops
	u.tickSkillshare(gl.Context{Ops: &ops}, &st)
	if u.ss.to.host != "box" || u.ss.inst != nil || !canceled || u.ss.sel != "" || u.ss.shown.Body != "" || u.ss.confirm || u.ss.ask != "" || len(u.ss.acts) != 0 {
		t.Fatalf("old machine state survived focus change: target %+v, install %v, canceled %v, selection %q", u.ss.to, u.ss.inst, canceled, u.ss.sel)
	}
}

func TestSkillsharePreviewIgnoresEarlierRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	changed := make(chan struct{}, 4)
	b := ssHostFake{hostFake: hostFake{NewFakeBackend()}, do: func(_ context.Context, c skillshare.Call, _ func(string)) skillshare.Reply {
		if c.Paths.Skills == "/old" {
			close(started)
			<-release
		}
		return skillshare.Reply{Preview: skillshare.Preview{Path: c.Paths.Skills, Body: c.Paths.Skills}}
	}}
	u := &ui{b: b, invalidate: func() { changed <- struct{}{} }}
	it := skillshare.Item{Kind: skillshare.Skills, Key: "global:skills:check"}
	read := func(path string) *skillshare.Read {
		r := &skillshare.Read{Status: &skillshare.StatusJSON{}}
		r.Status.Source.Path = path
		return r
	}
	u.ssOpen(it, read("/old"))
	<-started
	u.ss.to = ssTarget{host: "box"}
	u.ssOpen(it, read("/new"))
	<-changed
	u.ss.takePreview()
	if u.ss.shown.Path != "/new" {
		t.Fatalf("new preview: %+v", u.ss.shown)
	}
	close(release)
	<-changed
	u.ss.takePreview()
	if u.ss.shown.Path != "/new" {
		t.Fatalf("old machine replaced preview: %+v", u.ss.shown)
	}
}

func TestSkillshareProjectLookupOrder(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	changed := make(chan struct{}, 128)
	demo := skillshare.Demo()
	b := ssHostFake{hostFake: hostFake{NewFakeBackend()}, do: func(ctx context.Context, c skillshare.Call, step func(string)) skillshare.Reply {
		if c.Op == skillshare.OpProject {
			if c.Dir == "/folder-a" {
				close(started)
				<-release
			}
			return skillshare.Reply{Project: c.Dir}
		}
		return demo.Serve(ctx, c, step)
	}}
	st := b.State()
	u := &ui{b: b, invalidate: func() { changed <- struct{}{} }}
	u.nav.switchSession(&st, "s1")
	findPane(&st, u.nav.focused()).Cwd = "/folder-a"
	u.ssFollow(&st, true, true)
	<-started
	findPane(&st, u.nav.focused()).Cwd = "/folder-b"
	u.ssFollow(&st, true, true)
	// Every read and the two scheduling workers invalidate once.
	for range 2*(len(skillshare.Reads)+len(skillshare.Checks)) + 2 {
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatal("new folder was not read")
		}
	}
	close(release)
	select {
	case <-changed: // the stale project lookup has been discarded
	case <-time.After(5 * time.Second):
		t.Fatal("old folder lookup did not finish")
	}
	if got := u.ss.cur().store.View().Project; got != "/folder-b" {
		t.Fatalf("late lookup restored %q", got)
	}
}

func TestSkillshareAskStaysOnMachine(t *testing.T) {
	_, u, frame, _ := ssWindow(t)
	u.b = hostFake{u.b.(*FakeBackend)}
	st := u.b.State()
	u.nav.selectWorkspace(&st, "w1b", "i")
	u.toggleSkillshare(&st)
	waitReads(t, u, frame)
	u.ss.to = ssTarget{host: "box"}
	var ops op.Ops
	u.ssAsk(gl.Context{Ops: &ops}, &st, "/remote/skills/check")
	if len(u.review.queue) != 0 || u.modal.kind != modalSkillshare {
		t.Fatal("remote path sent to this computer's agent")
	}
}

func TestSkillshareOldHostVisible(t *testing.T) {
	b := ssHostFake{hostFake: hostFake{NewFakeBackend()}, on: func(string) (bool, bool) { return false, true }}
	u := &ui{b: b}
	st := b.State()
	u.nav.switchSession(&st, "s2")
	if !u.ssVisible(&st) {
		t.Fatal("old remote daemon has no way to show the update message")
	}
}

func TestSkillshareFollowsWithSidebarHidden(t *testing.T) {
	b := ssHostFake{hostFake: hostFake{NewFakeBackend()}, do: skillshare.Demo().Serve}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	u.nav.sidebarHidden, u.sidebarShown = true, true
	st := b.State()
	u.nav.switchSession(&st, "s1")
	u.toggleSkillshare(&st)
	u.nav.switchSession(&st, "s2")
	var ops op.Ops
	u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()})
	if u.ss.to.host != "box" {
		t.Fatalf("hidden sidebar stopped the popup following focus: %+v", u.ss.to)
	}
	t.Cleanup(func() { shownHost = "" })
}

// ssWindow is a window whose skillshare client answers from the captured
// fixtures, the popup open and its reads in.
func ssWindow(t *testing.T) (*FakeBackend, *ui, func(), func(...key.Event)) {
	old := skillshare.Default
	skillshare.Default = skillshare.Demo()
	t.Cleanup(func() { skillshare.Default = old })
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	keys := func(evs ...key.Event) {
		for _, e := range evs {
			r.Queue(e)
			frame()
			r.Queue(key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
			frame()
		}
	}
	frame()
	frame()
	return b, u, frame, keys
}

// waitReads draws frames until the popup's reads are in.
func waitReads(t *testing.T, u *ui, frame func()) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		frame()
		if v := u.ss.cur().store.View(); len(v.Items) > 0 && !u.ss.cur().store.Running() {
			frame()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the reads never landed")
}

// waitPreview takes the preview ssOpen reads off the UI goroutine.
func waitPreview(t *testing.T, u *ui) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		u.ss.takePreview()
		if u.ss.shown.Path != "" || u.ss.shown.Err != "" || u.ss.shown.MCP != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the preview never came")
}

// waitCard draws frames until this machine's Machines card is read.
func waitCard(t *testing.T, u *ui, frame func()) *skillshare.Machine {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		frame()
		u.ss.mu.Lock()
		c := u.ss.machine("").card
		u.ss.mu.Unlock()
		if c != nil {
			frame()
			return c
		}
	}
	t.Fatal("the machine card never came")
	return nil
}

// Machines reads a card per machine, here this one alone, with its
// version, the source two commits behind its remote, and Pull and sync;
// a click on its name points Overview at it.
func TestSkillshareMachines(t *testing.T) {
	_, u, frame, _ := ssWindow(t)
	u.toggleSkillshare(new(u.b.State()))
	waitReads(t, u, frame)
	u.ss.machView = true
	c := waitCard(t, u, frame)
	if c.Version == "" || c.Git.Behind != 2 || c.Git.Line() != "↓2 behind" || c.Targets != 2 {
		t.Fatalf("card %+v", *c)
	}
	if u.ss.acts["m::pull"] == nil || !strings.HasPrefix(u.ssChecked(time.Now()), "Checked ") {
		t.Fatal("the card has no Pull and sync, or the tab row no check time")
	}
	u.ss.acts["m::open"]()
	if u.ss.machView || u.ss.picked == nil || *u.ss.picked != "" {
		t.Fatalf("a click on the card: machines %v, picked %v", u.ss.machView, u.ss.picked)
	}
}

// Ctrl+Alt+S opens the popup and Escape closes it; the sidebar shows its
// button with what waits: deploy-notes, docs-server and format-check to
// sync, lint-rules to update.
func TestSkillshareOpensAndCounts(t *testing.T) {
	_, u, frame, keys := ssWindow(t)
	keys(key.Event{Name: "S", Modifiers: key.ModCtrl | key.ModAlt, State: key.Press})
	if u.modal.kind != modalSkillshare {
		t.Fatal("Ctrl+Alt+S should open the popup")
	}
	waitReads(t, u, frame)
	// why: waitReads can return between two reads, before every item's state lands; slow Windows process starts hit that gap
	for deadline := time.Now().Add(5 * time.Second); u.sidebar.SkillshareWaiting != 4 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		frame()
	}
	if !u.sidebar.Skillshare || u.sidebar.SkillshareWaiting != 4 {
		t.Fatalf("sidebar button %v, badge %d, want 4", u.sidebar.Skillshare, u.sidebar.SkillshareWaiting)
	}
	keys(key.Event{Name: key.NameEscape, State: key.Press})
	if u.modal.kind != modalNone {
		t.Fatal("Escape should close the popup")
	}
}

// Escape leaves a preview first, then closes.
func TestSkillshareEscapeLeavesPreview(t *testing.T) {
	_, u, frame, keys := ssWindow(t)
	u.toggleSkillshare(new(u.b.State()))
	waitReads(t, u, frame)
	v := u.ss.cur().store.View()
	it, _ := ssPick(v.Items, "code-review")
	u.ssOpen(it, ssReadOf(v, it))
	frame()
	keys(key.Event{Name: key.NameEscape, State: key.Press})
	if u.modal.kind != modalSkillshare || u.ss.sel != "" {
		t.Fatalf("first Escape: modal %v, sel %q", u.modal.kind, u.ss.sel)
	}
	keys(key.Event{Name: key.NameEscape, State: key.Press})
	if u.modal.kind != modalNone {
		t.Fatal("second Escape should close")
	}
}

func TestSkillshareEscapeAfterInstallBack(t *testing.T) {
	_, u, frame, keys := ssWindow(t)
	u.toggleSkillshare(new(u.b.State()))
	waitReads(t, u, frame)
	u.startInstall("")
	frame()
	frame()
	u.ss.acts["i-back"]()
	frame()
	frame()
	keys(key.Event{Name: key.NameEscape, State: key.Press})
	if u.modal.kind != modalNone {
		t.Fatal("Escape should close after the URL editor is removed")
	}
}

// Without the CLI on PATH nothing about skillshare shows: no palette
// entry, no sidebar button, and the key does nothing.
func TestSkillshareHiddenWithoutCLI(t *testing.T) {
	_, u, frame, keys := ssWindow(t)
	skillshare.Default = skillshare.Client{Installed: func() bool { return false }}
	frame()
	if u.sidebar.Skillshare {
		t.Fatal("the sidebar button shows without the CLI")
	}
	for _, e := range paletteEntries(u.nav.keys) {
		if e.action.Name == "toggle_skillshare" {
			t.Fatal("the palette lists skillshare without the CLI")
		}
	}
	keys(key.Event{Name: "S", Modifiers: key.ModCtrl | key.ModAlt, State: key.Press})
	if u.modal.kind == modalSkillshare {
		t.Fatal("the key opened the popup without the CLI")
	}
}

// Ask sends "audit this skill: <path>" to the focused agent and closes
// the popup.
func TestSkillshareAsk(t *testing.T) {
	b, u, frame, _ := ssWindow(t)
	st := b.State()
	var agent *model.Pane
	for i := range st.Panes {
		if agent == nil && model.IsAgent(st.Panes[i].Provider) {
			agent = &st.Panes[i]
		}
	}
	if agent == nil {
		t.Fatal("the fake has no agent pane")
	}
	u.nav.workspace = agent.WorkspaceID
	frame()
	u.toggleSkillshare(&st)
	waitReads(t, u, frame)
	u.ss.ask = "/home/demo/.config/skillshare/skills/code-review"
	frame()
	if u.modal.kind != modalNone {
		t.Fatal("Ask should close the popup")
	}
	// Queued, or already typed into the pane when its agent waits.
	queued := slices.ContainsFunc(u.review.queue, func(d delivery) bool {
		return d.send && d.pane == agent.ID && d.text == "audit this skill: /home/demo/.config/skillshare/skills/code-review"
	})
	if !queued && !strings.Contains(fmt.Sprint(b.Sent()), "audit this skill: /home/demo/.config/skillshare/skills/code-review") {
		t.Fatalf("not sent: queue %+v", u.review.queue)
	}
}

// TestSkillshareShots draws the popup's views offscreen and, with
// PITWALL_SHOTS set to a folder, writes them there as PNGs for a look.
func TestSkillshareShots(t *testing.T) {
	win, err := headless.NewWindow(1280, 800)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	_, u, frame, _ := ssWindow(t)
	st := u.b.State()
	u.toggleSkillshare(&st)
	waitReads(t, u, frame)
	dir := os.Getenv("PITWALL_SHOTS")
	shot := func(name string) {
		t.Helper()
		var ops op.Ops
		for range 3 { // the open animation settles
			ops.Reset()
			gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now().Add(time.Second)}
			u.layout(gtx)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		if dir == "" {
			return
		}
		f, err := os.Create(filepath.Join(dir, "skillshare-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	shot("overview")
	u.ss.library = true
	shot("library")
	for _, k := range []skillshare.Kind{skillshare.MCP, skillshare.Hooks} {
		u.ss.kind = k
		shot("library-" + string(k))
	}
	v := u.ss.cur().store.View()
	for _, name := range []string{"code-review", "demo-server", "reviewer"} {
		it, ok := ssPick(v.Items, name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		u.ssOpen(it, ssReadOf(v, it))
		waitPreview(t, u)
		shot("preview-" + name)
	}
	u.ss.confirm = true
	shot("preview-uninstall")
	u.ss.sel, u.ss.confirm, u.ss.machView = "", false, true
	waitCard(t, u, frame)
	shot("machines")
	u.ss.machView = false
	u.ss.sel, u.ss.confirm = "", false
	u.startInstall("")
	shot("install")
	b, err := os.ReadFile("../../skillshare/testdata/audit_blocked.json")
	if err != nil {
		t.Fatal(err)
	}
	a := skillshare.ParseAudit([]skillshare.AuditPart{{Folder: "skills/bad-skill", Stdout: string(b)}}, "/home/demo/src/skills-repo")
	u.ss.inst.url.SetText("example.com/acme/skills")
	u.ss.inst.step, u.ss.inst.audit = 2, &a
	shot("install-blocked")
}

// Install workers are driven with channels so close/discovery/audit overlap.
func TestSkillshareInstallLifecycle(t *testing.T) {
	for _, phase := range []string{"source", "close-discover", "close-audit"} {
		t.Run(phase, func(t *testing.T) {
			old := skillshare.Default
			c := skillshare.Demo()
			demo := c.Run
			started, release := make(chan struct{}), make(chan struct{})
			changed := make(chan struct{}, 128)
			var once sync.Once
			var callsMu sync.Mutex
			var installs [][]string
			block := func() { once.Do(func() { close(started); <-release }) }
			c.Clone = func(context.Context, string, string, string) error {
				if phase != "close-audit" {
					block()
				}
				return nil
			}
			c.Run = func(ctx context.Context, dir string, args ...string) skillshare.Result {
				if args[0] == "audit" {
					if phase == "close-audit" {
						block()
					}
					return skillshare.Result{Stdout: `{"results":[{"isBlocked":false}]}`}
				}
				if args[0] == "install" {
					if slices.Contains(args, "--dry-run") {
						return skillshare.Result{Stdout: `{"planned":[{"name":"a","path":"a"}]}`}
					}
					callsMu.Lock()
					installs = append(installs, slices.Clone(args))
					callsMu.Unlock()
					return skillshare.Result{Stdout: `{"skills":["a"]}`}
				}
				return demo(ctx, dir, args...)
			}
			skillshare.Default = c
			u := &ui{invalidate: func() { changed <- struct{}{} }}
			u.modal.open(modalSkillshare, "")
			u.startInstall("")
			in := u.ss.inst
			in.url.SetText("acme/source-a")
			u.ss.mu.Lock()
			u.ssDiscover("")
			u.ss.mu.Unlock()
			<-started
			if phase == "source" {
				// The editor is owned by the UI, never read by an install worker.
				in.url.SetText("acme/source-b")
			} else {
				u.closeSkillshare()
			}
			close(release)
			deadline := time.After(5 * time.Second)
			for {
				select {
				case <-changed:
				case <-deadline:
					t.Fatal("install worker did not finish")
				}
				u.ss.mu.Lock()
				done, work := in.step == 4, in.work
				u.ss.mu.Unlock()
				if phase != "source" && !work || phase == "source" && done && !work && !u.ss.cur().store.Running() {
					break
				}
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			if phase == "source" {
				if len(installs) != 1 || installs[0][1] != "acme/source-a" {
					t.Fatalf("installed an unaudited source: %v", installs)
				}
			} else if len(installs) != 0 {
				t.Fatalf("installed after close: %v", installs)
			}
			u.closeSkillshare()
			skillshare.Default = old
		})
	}
}

func TestSkillshareHookPreviewActions(t *testing.T) {
	_, u, frame, _ := ssWindow(t)
	u.toggleSkillshare(new(u.b.State()))
	waitReads(t, u, frame)
	v := u.ss.cur().store.View()
	it, ok := ssPick(v.Items, "format-check")
	if !ok {
		t.Fatal("hook missing")
	}
	u.ssOpen(it, ssReadOf(v, it))
	frame()
	if u.ss.acts["p-toggle"] == nil || u.ss.acts["p-uninstall"] == nil {
		t.Fatal("declared hook must have toggle and uninstall controls")
	}
	u.ss.acts["p-uninstall"]()
	frame()
	if !u.ss.confirm || u.ss.acts["p-uninstall-yes"] == nil {
		t.Fatal("hook removal must ask for confirmation")
	}
}

func TestSkillshareInstallScopeStaysWithPlan(t *testing.T) {
	_, u, frame, _ := ssWindow(t)
	u.toggleSkillshare(new(u.b.State()))
	waitReads(t, u, frame)
	u.startInstall("/project")
	in := u.ss.inst
	v := u.ss.cur().store.View()
	v.Project = "/project"
	var ops op.Ops
	gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(720, 640)), Now: time.Now()}
	rows := u.ssInstallRows(gtx, v)
	for _, row := range rows {
		row(gtx)
	}
	pickGlobal := u.ss.acts["i-scope:global"]
	if pickGlobal == nil {
		t.Fatal("source scope control missing")
	}
	in.step = 1 // Discovery finished before the old click was handled.
	pickGlobal()
	if in.scope != skillshare.Project {
		t.Fatal("a stale source-page click changed the plan's scope")
	}
}
