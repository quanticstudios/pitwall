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
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// DemoStates are the chat states ChatDemo draws, as the design shows them.
var DemoStates = []string{
	"following", "scrolled", "ready", "queued", "six-lines", "sending", "not-sent",
	"model-menu", "effort-menu", "model-pending", "long-name", "codex", "approval", "offline",
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
func (demoAPI) Approve(ctx context.Context, chat, request, decision, reason string) error {
	return nil
}
func (demoAPI) Input(ctx context.Context, chat, request string, answers map[string][]string) error {
	return nil
}
func (demoAPI) Older(ctx context.Context, pane string, before int64) ([]flow.Entry, bool, error) {
	return nil, false, nil
}

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

func demoTab() remote.Tab {
	return remote.Tab{Item: remote.Item{Pane: "p1", At: 1, Tab: "Fix the checkout race", Session: "demo", Agent: "claude",
		State: "working", Label: "Working", Detail: "Bash " + demoRun},
		ID: "ws1", Branch: "fix/checkout-race", Send: true, Picker: true}
}

// demoChat is the chat and its env in state, and whether its field takes
// the focus.
func demoChat(state string) (*chatView, chatEnv, bool) {
	c := newChat("p1")
	c.reset(phone.Entries{Entries: demoEntries(), Model: "claude-opus-5-5"})
	c.row(3).open = true
	c.cmp.effort = "high"
	t := demoTab()
	env := chatEnv{Tab: t, Open: true, Online: true,
		Models:  append(slices.Clone(remote.Models[:3]), append([]string{"opus[1m]"}, remote.Models[3:]...)...),
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
		k.pick = &pick{what: "model", value: "sonnet"}
	case "long-name":
		idleTab()
		c.data.Model, k.effort = "claude-opus-5-5[1m]", "max"
	case "codex":
		idleTab()
		env.Tab.Agent, env.Tab.Picker, env.Tab.Tab = "codex", false, "Add rate limits"
		c.data.Model = "gpt-6.1-sol"
		setDraft(&k.ed, "Rerun the limiter tests")
	case "approval":
		env.Tab.State, env.Tab.Label, env.Tab.Detail, env.Tab.Answer = "pending-approval", "Approval", demoRun, true
		c.data.Entries = c.data.Entries[:6]
	case "offline":
		env.Online, env.Offline = false, "Offline, reconnecting in 4 s"
		env.Retry = func() {}
		setDraft(&k.ed, "Then open a PR against main")
	}
	return &c, env, focus
}

// ChatShot draws the chat in state at 412x860dp, scale px per dp, and
// writes it to path as a PNG, with no window.
func ChatShot(th *theme.Theme, state, path string, scale float32) error {
	if !slices.Contains(DemoStates, state) {
		return errors.New("no demo state " + state + "; one of " + strings.Join(DemoStates, ", "))
	}
	c, env, focus := demoChat(state)
	size := image.Pt(int(412*scale), int(860*scale))
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
