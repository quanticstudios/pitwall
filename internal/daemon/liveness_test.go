package daemon

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// livePane is a fakePane with a settable screen and foreground group.
type livePane struct {
	*fakePane
	gmu     sync.Mutex
	screen  string
	moving  bool // every Snapshot differs, like a streaming reply
	n       int
	fgGroup atomic.Int64
}

func (p *livePane) Snapshot() vt.Grid {
	p.gmu.Lock()
	defer p.gmu.Unlock()
	s := p.screen
	if p.moving {
		p.n++
		s += "\n" + strconv.Itoa(p.n)
	}
	e := vt.New(60, 6, nil)
	e.Write([]byte(strings.ReplaceAll(s, "\n", "\r\n")))
	return e.Snapshot()
}

func (p *livePane) Foreground() int { return int(p.fgGroup.Load()) }

func (p *livePane) show(s string, moving bool) {
	p.gmu.Lock()
	p.screen, p.moving = s, moving
	p.gmu.Unlock()
}

const (
	idleScreen    = "  ⎿  Interrupted · What should Claude do instead?\n❯"
	spinnerScreen = "✢ Nesting… (9s · ↓ 232 tokens)\n❯"
	formScreen    = " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel · Tab to amend"
)

// liveDaemon opens one pane under a working agent whose foreground group is
// 100 and returns the daemon, the pane and its id.
func liveDaemon(t *testing.T, state model.AgentState) (*Daemon, *livePane, string) {
	t.Helper()
	oldDelay, oldStill, oldPoll := settleDelay, stillFor, livePoll
	settleDelay, stillFor, livePoll = 60*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { settleDelay, stillFor, livePoll = oldDelay, oldStill, oldPoll })

	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	var lp *livePane
	start := o.StartPane
	o.StartPane = func(c pane.Config) (Pane, error) {
		p, _ := start(c)
		lp = &livePane{fakePane: p.(*fakePane)}
		lp.fgGroup.Store(100)
		lp.show(idleScreen, false)
		return lp, nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.livenessLoop(ctx)
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	d.mu.Lock()
	ws := d.st.Workspaces[0].ID
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws}))
	d.mu.Lock()
	id := d.st.Panes[0].ID
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte(state)}))
	return d, lp, id
}

func (d *Daemon) stateOf(id string) model.AgentState {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := d.activityIndex(id); i >= 0 {
		return d.st.Activities[i].State
	}
	return ""
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// settled waits out every settle check started so far.
func settled() { time.Sleep(3 * settleDelay) }

func TestInterruptClearsWorking(t *testing.T) {
	d, _, id := liveDaemon(t, model.StateWorking)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.Input{Pane: id, Data: []byte("x")}))
	settled()
	if s := d.stateOf(id); s != model.StateWorking {
		t.Fatalf("typing ahead changed working to %q", s)
	}
	must(t, d.handle(ctx, proto.Input{Pane: id, Data: []byte("\x1b[27;1u")}))
	waitUntil(t, "interrupt clears working", func() bool { return d.stateOf(id) == "" })
}

func TestHookAfterInterruptWins(t *testing.T) {
	d, _, id := liveDaemon(t, model.StateWorking)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.Input{Pane: id, Data: []byte("\x1b")}))
	must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("working")}))
	settled()
	if s := d.stateOf(id); s != model.StateWorking {
		t.Fatalf("hook in time should keep working, got %q", s)
	}
}

func TestInterruptKeepsMovingScreen(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StateWorking)
	lp.show("one hundred three", true) // Claude hides its spinner while a reply streams
	must(t, d.handle(context.Background(), proto.Input{Pane: id, Data: []byte("\x03")}))
	settled()
	if s := d.stateOf(id); s != model.StateWorking {
		t.Fatalf("a streaming screen should stay working, got %q", s)
	}
}

func TestAnswerAtApproval(t *testing.T) {
	for _, tc := range []struct {
		screen string
		want   model.AgentState
	}{
		{formScreen, model.StatePendingApproval}, // moved the cursor, form still up
		{spinnerScreen, model.StateWorking},      // approved a long tool
		{idleScreen, ""},                         // denied
	} {
		d, lp, id := liveDaemon(t, model.StatePendingApproval)
		lp.show(tc.screen, false)
		must(t, d.handle(context.Background(), proto.Input{Pane: id, Data: []byte("1")}))
		settled()
		if s := d.stateOf(id); s != tc.want {
			t.Errorf("%q: got %q, want %q", tc.screen, s, tc.want)
		}
	}
}

func TestForegroundLeavingAgentClears(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StateCompleted)
	time.Sleep(5 * livePoll)
	if d.stateOf(id) != model.StateCompleted {
		t.Fatal("unchanged foreground dropped the activity")
	}
	lp.fgGroup.Store(200) // the shell took the terminal back
	waitUntil(t, "foreground change clears", func() bool { return d.stateOf(id) == "" })
}

func TestPaneExitClears(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StateWorking)
	lp.Close() // the foreground read stays 100, so only the exit can clear it
	waitUntil(t, "exit clears", func() bool { return d.stateOf(id) == "" })
}

func TestIsInterrupt(t *testing.T) {
	for in, want := range map[string]bool{
		"\x1b": true, "\x03": true, "\x1b[27;1u": true, "\x1b[99;5u": true, "\x1b[99;5:1u": true,
		"\x1b[27;1:3u": false, "\x1b\x1b": false, "\x1b[A": false, "c": false, "\x1b[99;1u": false,
	} {
		if isInterrupt([]byte(in)) != want {
			t.Errorf("isInterrupt(%q) != %v", in, want)
		}
	}
}
