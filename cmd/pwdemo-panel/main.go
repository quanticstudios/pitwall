// Command pwdemo-panel shows the side panel against fake feeds that mirror
// the approved mock: Claude with a plan, subagents and a pending approval,
// Codex waiting on one, pi done. Keys 1, 2 and 3 switch agents; the Claude
// feed changes every few seconds, like the mock's simulation.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// showCost and blank are the -cost and -blank flags.
var (
	showCost bool
	blank    string
)

func main() {
	agent := flag.String("agent", "claude", "claude, codex or pi")
	view := flag.String("view", "flow", "flow, subagents, plan, changes or timeline")
	detail := flag.Int("detail", -1, "in subagents, the subagent to open")
	static := flag.Bool("static", false, "no simulated updates")
	file := flag.String("file", "", "read this session file of -agent through flow.Watch instead of the fake feed")
	flag.BoolVar(&showCost, "cost", false, "show token use in dollars, as [usage] show_cost does")
	flag.StringVar(&blank, "blank", "", "loading: git and the session file not read yet; empty: read, with nothing in them")
	flag.Parse()
	go func() {
		w := new(app.Window)
		w.Option(app.Title("pwdemo-panel"), app.Size(unit.Dp(panel.Width), unit.Dp(962)))
		if err := run(w, *agent, panel.View(*view), *detail, *static, *file); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, agent string, view panel.View, detail int, static bool, file string) error {
	th := theme.Dark()
	start := time.Now()
	var mu sync.Mutex
	inputs := map[string]*panel.Input{"claude": claude(start), "codex": codex(start), "pi": pi(start)}
	if file != "" {
		prov := model.Provider(agent)
		in := &panel.Input{
			Pane:     &model.Pane{ID: "p1", Provider: prov, Transcript: file},
			Activity: &model.Activity{PaneID: "p1", Provider: prov, State: model.StateWorking, UpdatedAt: start},
			Feed:     &flow.Feed{Provider: prov},
		}
		inputs[agent] = in
		flow.Watch(context.Background(), prov, file, func(f flow.Feed) {
			mu.Lock()
			in.Feed = &f
			mu.Unlock()
			w.Invalidate()
		})
	} else if !static {
		go simulate(&mu, inputs["claude"], w.Invalidate)
	}
	var p panel.Panel
	p.SetView(view, detail)
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			for {
				ev, ok := gtx.Event(key.Filter{Name: "1"}, key.Filter{Name: "2"}, key.Filter{Name: "3"})
				if !ok {
					break
				}
				if k, ok := ev.(key.Event); ok && k.State == key.Press {
					agent = map[key.Name]string{"1": "claude", "2": "codex", "3": "pi"}[k.Name]
				}
			}
			mu.Lock()
			in := *inputs[agent]
			in.Now = time.Now()
			in.ShowCost = showCost
			switch blank {
			case "loading":
				in.Feed, in.Git, in.Files, in.WaitGit, in.WaitFeed = nil, false, nil, true, true
			case "empty":
				in.Feed, in.Files = &flow.Feed{Provider: in.Pane.Provider}, nil
			}
			p.Layout(gtx, th, in)
			mu.Unlock()
			e.Frame(gtx.Ops)
		}
	}
}

func claude(start time.Time) *panel.Input {
	t0 := start.Add(-(3*time.Minute + 12*time.Second))
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	ago := func(d time.Duration) time.Time { return start.Add(-d) }
	call := func(s int, tool, arg string) flow.Call { return flow.Call{Time: at(s), Tool: tool, Arg: arg} }
	sub := func(id, name, typ, prompt, latest string, s, took int, calls ...flow.Call) flow.Subagent {
		sa := flow.Subagent{ID: id, Name: name, Type: typ, Prompt: prompt, Latest: latest, Start: at(s), Calls: calls}
		if took > 0 {
			sa.End = sa.Start.Add(time.Duration(took) * time.Second)
		}
		return sa
	}
	loadStart := 67
	subat := func(d int) time.Time { return at(loadStart + d) }
	feed := &flow.Feed{
		Provider: model.ProviderClaude,
		Turns: []flow.Turn{
			{Prompt: "Explore the auth middleware", Start: ago(80 * time.Minute), End: ago(70 * time.Minute)},
			{Prompt: "Write the rate limit design", Start: ago(60 * time.Minute), End: ago(48 * time.Minute)},
			{Prompt: "Implement the bucket from the design, then run the tests", Start: t0, Subagents: []int{2, 3},
				Calls: []flow.Call{
					call(4, "Read", "internal/http/router.go"), call(5, "Read", "internal/http/middleware.go"), call(6, "Read", "internal/http/context.go"),
					call(7, "Read", "docs/ratelimit.md"), call(8, "Read", "internal/http/auth.go"), call(9, "Read", "go.mod"),
					call(19, "Task", "explore: Find every handler that skips auth"), call(21, "Task", "general: Draft a load test for /orders"),
					call(150, "Bash", "go test ./internal/ratelimit/..."), call(168, "Edit", "internal/ratelimit/bucket.go"),
					call(169, "Edit", "internal/http/middleware.go"), call(170, "Write", "internal/ratelimit/bucket_test.go"),
					call(171, "TodoWrite", "6 steps"), call(172, "Read", "internal/http/router.go"), call(173, "Grep", "SkipAuth"),
					call(174, "Read", "internal/webhooks/stripe.go"), call(175, "Edit", "internal/http/router.go"),
					call(176, "Bash", "go vet ./..."), call(177, "Bash", "go build ./..."), call(178, "Read", "Makefile"),
					call(179, "Edit", "internal/http/middleware.go"), call(180, "Read", "internal/http/middleware_test.go"),
					{Time: start.Add(-12 * time.Second), Tool: "Bash", Arg: "go test ./internal/http/...", Running: true},
				}},
		},
		Plan: []flow.Step{
			{Text: "Design token bucket per API key", State: flow.StepDone}, {Text: "Implement bucket with refill", State: flow.StepDone},
			{Text: "Wire limiter into middleware", State: flow.StepDone}, {Text: "Exempt /healthz and /metrics", State: flow.StepActive},
			{Text: "Load test /orders"}, {Text: "Update API docs with limits"},
		},
		Subagents: []flow.Subagent{
			sub("a3f1", "Middleware state", "Explore", "How does request state flow through internal/http middleware?",
				"State rides on the request context under typed keys in context.go. The API key is set by auth before any other middleware runs.", -3780, 48,
				flow.Call{Time: at(-3778), Tool: "Read", Arg: "internal/http/middleware.go"}, flow.Call{Time: at(-3769), Tool: "Read", Arg: "internal/http/context.go"}),
			sub("b7c2", "Bucket design review", "general-purpose", "Review docs/ratelimit.md and list anything a token bucket per key would get wrong.",
				"The design has no burst allowance, so a client that sends 10 requests at once after an idle minute gets limited. Add a burst of 2x the per-second rate.", -3150, 220,
				flow.Call{Time: at(-3147), Tool: "Read", Arg: "docs/ratelimit.md"}, flow.Call{Time: at(-3080), Tool: "WebFetch", Arg: "token bucket burst semantics"}),
			sub("c9d4", "Auth handler recon", "Explore", "Find every HTTP handler that skips the auth middleware and say which ones take untrusted input.",
				"Three handlers skip auth: /healthz, /metrics and /webhooks/stripe. Only the webhook takes untrusted input. Exempt the first two from the limiter and limit the webhook by source IP.", 19, 62,
				call(20, "Grep", "SkipAuth|noAuth in internal/http"), call(25, "Read", "internal/http/router.go"), call(39, "Read", "internal/webhooks/stripe.go")),
			sub("e1f8", "Load test for /orders", "general-purpose",
				"Write a load test for GET /orders that drives 200 requests a second across 50 API keys for 30 seconds and asserts that requests past the burst get 429. Do not run it.",
				"Reusing the checkout load test harness. Splitting keys into 50 workers so each one hits its own bucket.", loadStart, 0,
				flow.Call{Time: subat(2), Tool: "Read", Arg: "internal/http/router.go"}, flow.Call{Time: subat(9), Tool: "Glob", Arg: "loadtest/**/*.go"},
				flow.Call{Time: subat(15), Tool: "Read", Arg: "loadtest/checkout_test.go"}, flow.Call{Time: subat(41), Tool: "Write", Arg: "loadtest/orders_test.go +160"},
				flow.Call{Time: subat(90), Tool: "Edit", Arg: "loadtest/orders_test.go +30 -35", Running: true}),
		},
		Usage: flow.Usage{Model: "claude-opus-5-5", Context: 312_400, Models: map[string]flow.Tokens{
			"claude-opus-5-5":  {Input: 2_180, Output: 61_250, CacheRead: 4_812_000, CacheWrite: 298_400, CacheWrite1h: 298_400},
			"claude-haiku-4-5": {Input: 41_300, Output: 9_870, CacheRead: 210_500, CacheWrite: 38_100},
		}},
	}
	return &panel.Input{
		Pane:     &model.Pane{ID: "p1", Provider: model.ProviderClaude, AgentMode: "bypass permissions", Transcript: "/fake"},
		Activity: &model.Activity{PaneID: "p1", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: start},
		Feed:     feed,
		Branch:   model.BranchStats{Additions: 412, Deletions: 38},
		Ports:    model.PortBlock{First: 3010, Last: 3019},
		Git:      true, Base: "main",
		Files: []gitstat.FileStat{
			{Path: "internal/ratelimit/bucket.go", Add: 86, Status: 'A'}, {Path: "internal/ratelimit/bucket_test.go", Add: 112, Status: 'A'},
			{Path: "internal/http/middleware.go", Add: 24, Del: 3, Status: 'M'}, {Path: "loadtest/orders_test.go", Add: 190, Del: 35, Status: '?'},
		},
		Decide: "jev",
	}
}

func codex(start time.Time) *panel.Input {
	t0 := start.Add(-(6*time.Minute + 40*time.Second))
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	calls := []flow.Call{
		{Time: at(3), Tool: "exec", Arg: "rg -n 'func List' internal/orders"}, {Time: at(9), Tool: "exec", Arg: "sed -n 1,120p internal/orders/list.go"},
		{Time: at(30), Tool: "exec", Arg: "sed -n 1,80p internal/orders/list_test.go"}, {Time: at(200), Tool: "apply_patch", Arg: "list.go"},
		{Time: at(252), Tool: "apply_patch", Arg: "list_test.go"}, {Time: at(300), Tool: "exec", Arg: "go test ./internal/orders/..."},
		{Time: at(340), Tool: "exec", Arg: "go vet ./..."}, {Time: at(370), Tool: "exec", Arg: "git diff --stat"},
		{Time: at(400), Tool: "exec", Arg: "psql \"$DATABASE_URL\" -c 'EXPLAIN SELECT * FROM orders …'", Running: true},
	}
	return &panel.Input{
		Pane: &model.Pane{ID: "p2", Provider: model.ProviderCodex, Transcript: "/fake"},
		Activity: &model.Activity{PaneID: "p2", Provider: model.ProviderCodex, State: model.StatePendingApproval, UpdatedAt: at(400),
			Detail: "psql … EXPLAIN SELECT * FROM orders", Advice: "allow", AdviceP: 0.96, AdviceRule: "reads the database", Urgency: "now"},
		Feed: &flow.Feed{Provider: model.ProviderCodex, Turns: []flow.Turn{{Prompt: "Add cursor pagination to GET /orders", Start: t0, Calls: calls}},
			Usage: flow.Usage{Model: "gpt-6.1-sol", Context: 221_700, Window: 258_400, Models: map[string]flow.Tokens{
				"gpt-6.1-sol": {Input: 96_400, Output: 18_900, CacheRead: 1_402_000},
			}}},
		Branch: model.BranchStats{Additions: 128, Deletions: 21},
		Git:    true, Base: "main",
		Files:  []gitstat.FileStat{{Path: "internal/orders/list.go", Add: 92, Del: 14, Status: 'M'}, {Path: "internal/orders/list_test.go", Add: 36, Del: 7, Status: 'M'}},
		Decide: "jev",
	}
}

func pi(start time.Time) *panel.Input {
	t0 := start.Add(-12 * time.Minute)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	var calls []flow.Call
	for i, f := range []string{"src/parser/tokenizer.py", "src/parser/expr.py", "src/parser/ast.py", "tests/test_parser.py", "README.md", "CHANGES.md"} {
		calls = append(calls, flow.Call{Time: at(2 + i), Tool: "read", Arg: f})
	}
	calls = append(calls,
		flow.Call{Time: at(60), Tool: "write", Arg: "src/parser/tokenizer.ts"}, flow.Call{Time: at(120), Tool: "write", Arg: "src/parser/expr.ts"},
		flow.Call{Time: at(160), Tool: "bash", Arg: "npm test", Failed: true}, flow.Call{Time: at(200), Tool: "edit", Arg: "src/parser/expr.ts"},
		flow.Call{Time: at(240), Tool: "edit", Arg: "src/parser/expr.ts"}, flow.Call{Time: at(280), Tool: "bash", Arg: "npm test"},
		flow.Call{Time: at(300), Tool: "edit", Arg: "CHANGES.md"}, flow.Call{Time: at(320), Tool: "bash", Arg: "git diff --stat"},
	)
	end := at(331)
	return &panel.Input{
		Pane:     &model.Pane{ID: "p3", Provider: model.ProviderPi, Transcript: "/fake"},
		Activity: &model.Activity{PaneID: "p3", Provider: model.ProviderPi, State: model.StateCompleted, UpdatedAt: start.Add(-7 * time.Minute)},
		Feed: &flow.Feed{Provider: model.ProviderPi, Turns: []flow.Turn{
			{Prompt: "Read the old parser and list its quirks", Start: t0.Add(-30 * time.Minute), End: t0.Add(-25 * time.Minute)},
			{Prompt: "Port it and keep the tests green", Start: t0, End: end, Calls: calls,
				Reply: "Ported the tokenizer and expression parser. All 41 tests pass."},
		}},
		Branch: model.BranchStats{Additions: 57, Deletions: 3},
		Git:    true, Base: "main",
		Files:  []gitstat.FileStat{{Path: "src/parser/tokenizer.ts", Add: 41, Del: 2, Status: 'M'}, {Path: "src/parser/expr.ts", Add: 16, Del: 1, Status: 'M'}},
		Decide: "jev",
	}
}

// simulate walks the Claude feed through the mock's live steps, one every
// six seconds: a plan step finishes, the subagent reports, it finishes,
// and an approval comes in. The window draws under mu.
func simulate(mu *sync.Mutex, in *panel.Input, invalidate func()) {
	steps := []func(){
		func() {
			in.Feed.Plan[3].State, in.Feed.Plan[4].State = flow.StepDone, flow.StepActive
			t := &in.Feed.Turns[2]
			t.Calls[len(t.Calls)-1].Running = false
			t.Calls = append(t.Calls, flow.Call{Time: time.Now(), Tool: "Edit", Arg: "internal/http/router.go", Running: true})
		},
		func() {
			s := &in.Feed.Subagents[3]
			s.Calls[len(s.Calls)-1].Running = false
			s.Calls = append(s.Calls, flow.Call{Time: time.Now(), Tool: "Bash", Arg: "go vet ./loadtest/...", Running: true})
			s.Latest = "Test written. Running go vet before I hand it back."
		},
		func() {
			s := &in.Feed.Subagents[3]
			s.Calls[len(s.Calls)-1].Running = false
			s.End = time.Now()
			s.Latest = "Wrote loadtest/orders_test.go: 200 requests a second for 30s across 50 keys, expecting 429s past the burst. go vet is clean. Not run yet."
		},
		func() {
			t := &in.Feed.Turns[2]
			t.Calls[len(t.Calls)-1].Running = false
			t.Calls = append(t.Calls, flow.Call{Time: time.Now(), Tool: "Bash", Arg: "go run ./loadtest -rps 200", Running: true})
			*in.Activity = model.Activity{PaneID: "p1", Provider: model.ProviderClaude, State: model.StatePendingApproval, UpdatedAt: time.Now(),
				Detail: "go run ./loadtest -rps 200", Advice: "allow", AdviceP: 0.71, AdviceRule: "starts a server", Urgency: "soon"}
		},
	}
	for _, step := range steps {
		time.Sleep(6 * time.Second)
		mu.Lock()
		step()
		mu.Unlock()
		invalidate()
	}
}
