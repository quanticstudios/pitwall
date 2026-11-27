//go:build !android

package mobile

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"slices"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// DemoStates are the chat states ChatDemo draws, as the design shows them.
var DemoStates = []string{
	"following", "scrolled", "ready", "queued", "six-lines", "sending", "not-sent",
	"model-menu", "effort-menu", "model-pending", "long-name", "codex", "approval", "offline", "host", "screen",
	"agy", "agy-model-menu", "agy-mode-menu", "away", "away-short",
	"idle", "needs-you", "queued-two", "tab-menu", "reports",
	"long-turn", "edits", "failed", "work-open",
}

// demoAPI answers the chat's calls after a short wait, as a computer would.
type demoAPI struct{}

func (demoAPI) Send(ctx context.Context, pane, text string) (uint64, error) {
	time.Sleep(400 * time.Millisecond)
	return 7, nil
}
func (demoAPI) Command(ctx context.Context, pane, what, value string) error {
	time.Sleep(300 * time.Millisecond)
	return nil
}
func (demoAPI) Answer(ctx context.Context, pane string, at int64, allow bool) error { return nil }
func (demoAPI) CloseTab(ctx context.Context, tab string) error                      { return nil }
func (demoAPI) Restart(ctx context.Context, pane string) error                      { return nil }
func (demoAPI) Approve(ctx context.Context, chat, request, decision, reason string) error {
	return nil
}
func (demoAPI) Input(ctx context.Context, chat, request string, answers map[string][]string) error {
	return nil
}
func (demoAPI) Older(ctx context.Context, pane string, before int64) ([]flow.Entry, bool, error) {
	return nil, false, nil
}
func (demoAPI) Screen(ctx context.Context, pane string) (string, error) { return demoScreen, nil }
func (demoAPI) Key(ctx context.Context, pane, key string) error         { return nil }
func (demoAPI) Stop(ctx context.Context, pane string) error             { return nil }
func (demoAPI) AwayDismiss(context.Context, string) error               { return nil }
func (demoAPI) View(context.Context, string, bool) error                { return nil }
func (demoAPI) Queue(context.Context, string, string, string, int) error {
	return nil
}

// demoScreen is Claude Code's autocompact dialog, a menu pitwall does not
// read, as the Screen view shows it.
const demoScreen = ` ▐▛███▜▌   Claude Code v2.1.330
▝▜█████▛▘  Opus 5.5 · Claude Max
  ▘▘ ▝▝    ~/src/shop

> Fix the checkout race

● Found it. Add appends to c.items while Total ranges over it.

╭──────────────────────────────────────────────────────────╮
│ We recommend compacting earlier                          │
│                                                          │
│ Autocompact at 400k tokens?                              │
│                                                          │
│ ❯ 1. Yes, compact at 400k                                │
│   2. No, keep the default                                │
│                                                          │
│ Enter to confirm · esc not now                           │
╰──────────────────────────────────────────────────────────╯`

const (
	demoDraft = "Also run the cart benchmarks before you commit, and paste the numbers here"
	demoLong  = "Also run the cart benchmarks before you commit. Paste the before and after numbers here, ns/op and allocs/op for Add and Total, with -count=10 so the variance shows. If Total got slower than 5%, say so before you open the PR, and keep the lock in Add only."
	demoRun   = "go test -race -count=50 ./internal/checkout/..."
)

func demoEntries() []flow.Entry {
	return []flow.Entry{
		{Seq: 1, Kind: flow.EntryUser, Text: "The checkout total is off under load. Find the race and fix it."},
		{Seq: 2, Kind: flow.EntryAssistant, Text: "I'll run the test under the race detector first, then read the cart code it points at."},
		{Seq: 3, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test -race -count=20 ./internal/checkout/...", Done: true, Failed: true,
			Out: "WARNING: DATA RACE\nWrite by goroutine 31:\n  checkout.(*Cart).Add()\n      cart.go:42 +0x64\nPrevious read by goroutine 30:\n  checkout.(*Cart).Total()\n      cart.go:57 +0x3c\nFAIL  example.com/shop/internal/checkout"},
		{Seq: 4, Kind: flow.EntryTool, Tool: "Read", Arg: "cart.go", Done: true, Out: "package checkout"},
		{Seq: 5, Kind: flow.EntryAssistant, Text: "Found it. `Add` appends to `c.items` while `Total` ranges over it, and neither takes a lock. The fix:\n\n1. Give `Cart` a `sync.Mutex`.\n2. Hold it in `Add`, `Remove` and `Total`.\n\n```go\nfunc (c *Cart) Total() int {\n    c.mu.Lock()\n    defer c.mu.Unlock()\n    ...\n}\n```"},
		{Seq: 6, Kind: flow.EntryTool, Tool: "Edit", Arg: "cart.go", Done: true},
		{Seq: 7, Kind: flow.EntryTool, Tool: "Bash", Arg: demoRun},
	}
}

// demoTurn is one finished turn for the reading states: a prompt, the
// work between, and the reply, over 2m 14s from at.
func demoTurn(state string, at time.Time) []flow.Entry {
	ms := func(s int) int64 { return at.Add(time.Duration(s) * time.Second).UnixMilli() }
	es := []flow.Entry{
		{Seq: 1, Kind: flow.EntryUser, Text: "Make the login test stop racing the session cookie.", At: ms(0)},
		{Seq: 2, Kind: flow.EntryAssistant, Text: "I'll run the login tests a few times to catch the race first.", At: ms(3)},
		{Seq: 3, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test -race -count=20 ./internal/auth/...", Done: true, At: ms(5), Out: "ok  example.com/app/internal/auth  4.112s"},
		{Seq: 4, Kind: flow.EntryTool, Tool: "Grep", Arg: "SetCookie", Done: true, At: ms(31), Out: "login.go:88\nsession.go:41"},
		{Seq: 5, Kind: flow.EntryTool, Tool: "Read", Arg: "login.go", Path: "internal/auth/login.go", Done: true, At: ms(34)},
		{Seq: 6, Kind: flow.EntryTool, Tool: "Read", Arg: "session.go", Path: "internal/auth/session.go", Done: true, At: ms(37)},
	}
	switch state {
	case "long-turn":
		for i, a := range []string{"login_test.go", "cookie.go", "clock.go", "refresh.go", "middleware.go", "store.go"} {
			es = append(es, flow.Entry{Seq: int64(7 + i), Kind: flow.EntryTool, Tool: "Read", Arg: a, Done: true, At: ms(40 + 9*i)})
		}
		es = append(es, flow.Entry{Seq: 13, Kind: flow.EntryAssistant, At: ms(134), Text: "The race is in the refresh path, not the login. `refresh` reads the cookie before `store.Save` has written it back, so the second request sees the old expiry.\n\nNothing to change in `login.go`. Want me to move the save before the write, or lock the session for the whole refresh?"})
	case "edits", "failed":
		es = append(es,
			flow.Entry{Seq: 7, Kind: flow.EntryTool, Tool: "Edit", Arg: "login.go", Path: "internal/auth/login.go", Done: true, At: ms(52), Add: 6, Del: 2},
			flow.Entry{Seq: 8, Kind: flow.EntryTool, Tool: "Edit", Arg: "login.go", Path: "internal/auth/login.go", Done: true, At: ms(61), Add: 5, Del: 1},
			flow.Entry{Seq: 9, Kind: flow.EntryTool, Tool: "Edit", Arg: "login_test.go", Path: "internal/auth/login_test.go", Done: true, At: ms(70), Add: 7, Del: 1},
			flow.Entry{Seq: 10, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test -race ./internal/auth/...", Done: true, Failed: state == "failed", At: ms(81),
				Out: map[bool]string{true: "--- FAIL: TestLoginRefresh (1.00s)\n    login_test.go:57: expiry 14:02:11, want 14:02:12\nFAIL", false: "ok  example.com/app/internal/auth  3.904s"}[state == "failed"]},
		)
		reply := "The race is gone: `Login` now writes the session before it sets the cookie, and the test waits on the write."
		if state == "failed" {
			reply = "The race is gone. `TestLoginRefresh` still fails by one second. Pad the expiry by the skew, or move the check to the client?"
		}
		es = append(es, flow.Entry{Seq: 11, Kind: flow.EntryAssistant, Text: reply, At: ms(134)})
	}
	return es
}

func demoTab() remote.Tab {
	return remote.Tab{Item: remote.Item{Pane: "p1", At: 1, Tab: "Fix the checkout race", Session: "demo", Agent: "claude",
		State: "working", Label: "Working", Detail: "Bash " + demoRun},
		ID: "ws1", Branch: "fix/checkout-race", Send: true, Picker: true, Modes: remote.ModesOf("claude", "default"), Mode: "default",
		Spinner: "Flibbertigibbeting… 11s · ↓ 789 tokens · thinking"}
}

// demoChat is the chat and its env in state, and whether its field takes
// the focus.
func demoChat(state string) (*chatView, chatEnv, bool) {
	c := newChat("p1")
	c.reset(phone.Entries{Entries: demoEntries(), Model: "claude-opus-5-5"})
	c.row(3).open = true
	c.cmp.effort = "high"
	t := demoTab()
	env := chatEnv{Tab: t, Open: true, Online: true, CanScreen: true, CanKey: true, CanStop: true,
		Models:  remote.Models,
		Efforts: remote.Efforts,
		Now:     time.Unix(1_800_000_000, 0)}
	env.Do = func(call func(context.Context, chatAPI) error, done func(error)) {
		done(call(context.Background(), demoAPI{}))
	}
	focus := false
	k := &c.cmp
	idleTab := func() { env.Tab.State, env.Tab.Label, env.Tab.Detail = "completed", "Done", "" }
	switch state {
	case "scrolled":
		c.loading, c.fresh = true, true
		c.list.Position = layout.Position{BeforeEnd: true}
	case "ready":
		idleTab()
		setDraft(&k.ed, demoDraft)
		focus = true
	case "queued":
		env.Tab.Queued = 1
		c.data.Pending = []*phone.Pending{{Text: "Run the benchmarks before you commit", Sent: true, Queued: true, ID: 3}}
		setDraft(&k.ed, "Then open a PR against main")
		focus = true
	case "six-lines":
		idleTab()
		setDraft(&k.ed, demoLong)
		focus = true
	case "sending":
		setDraft(&k.ed, "Then open a PR against main")
		k.sending = true
		env.Do = func(func(context.Context, chatAPI) error, func(error)) {}
	case "not-sent":
		env.Tab.State, env.Tab.Label, env.Tab.Detail = "", "", ""
		c.data.Pending = []*phone.Pending{{Text: "Then open a PR against main", Sent: true, Err: "the agent exited", ID: 4}}
	case "model-menu":
		k.menu = "model"
	case "effort-menu":
		k.menu = "effort"
	case "model-pending":
		k.picks = []*pick{{what: "model", value: "sonnet"}}
	case "long-name":
		idleTab()
		c.data.Model, k.effort = "claude-opus-5-5[1m]", "max"
	case "codex":
		idleTab()
		env.Tab.Agent, env.Tab.Picker, env.Tab.Tab = "codex", false, "Add rate limits"
		c.data.Model = "gpt-6.1-sol"
		setDraft(&k.ed, "Rerun the limiter tests")
	case "agy", "agy-model-menu", "agy-mode-menu":
		idleTab()
		env.Tab.Agent, env.Tab.Picker, env.Tab.Tab = "antigravity", false, "Speed up the search index"
		env.Tab.Models = []string{"gemini-4-neon", "gemini-4-carbon", "gemini-3.8-flash", "claude-opus-5-5"}
		env.Tab.Efforts, env.Tab.Modes = remote.AgyEfforts, remote.AgyModes
		env.Tab.Model, env.Tab.Effort, env.Tab.Mode = "Gemini 4 Neon", "high", "auto"
		k.effort, c.data.Model = "", ""
		k.menu = map[string]string{"agy-model-menu": "model", "agy-mode-menu": "mode"}[state]
	case "idle":
		idleTab()
	case "tab-menu":
		env.CanClose, c.menu.open = true, true
	case "needs-you":
		env.Tab.State, env.Tab.Label, env.Tab.Detail = "awaiting-input", "Input", ""
	case "queued-two":
		env.Tab.Queued = 2
		env.Tab.Queue = []remote.QueueRow{{ID: "q1", Text: "Run the cart benchmarks before you commit"}, {ID: "q2", Text: "Then open a PR against main and paste the numbers in its body"}}
	case "approval":
		env.Tab.State, env.Tab.Label, env.Tab.Detail, env.Tab.Answer = "pending-approval", "Approval", demoRun, true
		c.data.Entries = c.data.Entries[:6]
	case "away", "away-short":
		idleTab()
		now := env.Now
		env.Tab.Away = &model.Away{Chat: "p1", Since: now.Add(-47 * time.Minute), At: now, Replies: 2, Turns: 1,
			Edited: []string{"cart.go", "cart_test.go"}, EditedN: 2, Commands: 3, Failed: "go test -race ./cart/...", Tests: "failed", Item: "x", Seq: map[string]int64{"away": 2, "away-short": 6}[state],
			Status: "Add no longer races, but one test still fails under -race.", Needs: "Say whether a mutex per cart is fine, or you want a channel."}
	case "reports":
		idleTab()
		c.data.Entries = c.data.Entries[:6]
		env.Tab.Reports = demoReports(env.Now)
	case "long-turn", "edits", "failed", "work-open":
		env.Tab.State, env.Tab.Label, env.Tab.Detail, env.Tab.Tab = "completed", "Done", "", "Login cookie race"
		turn := state
		if state == "work-open" {
			turn = "failed"
		}
		c.reset(phone.Entries{Entries: demoTurn(turn, env.Now.Add(-10*time.Minute)), Model: "claude-opus-5-5"})
		if state == "work-open" {
			c.work(2).open = true
			c.row(10).open = true
		}
	case "screen":
		c.scr.show()
	case "host":
		env.Host = "box"
		env.Hosts = []sidebar.HostInfo{{Up: true}, {Name: "box", Up: true}}
	case "offline":
		env.Online, env.Offline = false, "Offline, reconnecting in 4 s"
		env.Retry = func() {}
		setDraft(&k.ed, "Then open a PR against main")
	}
	return &c, env, focus
}

// ShotWidth is ChatShot's width in dp; pwdemo-chat's -width sets it.
var ShotWidth = 412

// ChatShot draws the chat in state at ShotWidth x 860dp, scale px per dp,
// and writes it to path as a PNG, with no window.
func ChatShot(th *theme.Theme, state, path string, scale float32) error {
	return ChatShotAt(th, state, path, scale, float32(ShotWidth))
}

// ChatShotAt is ChatShot at width dp across.
func ChatShotAt(th *theme.Theme, state, path string, scale, width float32) error {
	if !slices.Contains(DemoStates, state) {
		return errors.New("no demo state " + state + "; one of " + strings.Join(DemoStates, ", "))
	}
	c, env, focus := demoChat(state)
	size := image.Pt(int(width*scale), int(860*scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return err
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for i := range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: env.Now,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size)}
		if i == 0 && focus {
			gtx.Execute(key.FocusCmd{Tag: &c.cmp.ed})
		}
		c.layout(gtx, th, env)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ChatDemo runs the chat in state in w, with demo data and a fake
// computer, until w closes. Esc or Back closes a menu, then the keyboard.
func ChatDemo(w *app.Window, th *theme.Theme, state string) error {
	c, env, focus := demoChat(state)
	env.Now = time.Time{}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			if focus {
				gtx.Execute(key.FocusCmd{Tag: &c.cmp.ed})
				focus = false
			}
			for {
				ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
				if !ok {
					break
				}
				if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
					c.handleBack(gtx)
				}
			}
			if c.layout(gtx, th, env) {
				c, env, _ = demoChat(state)
				env.Now = time.Time{}
			}
			e.Frame(gtx.Ops)
		}
	}
}
