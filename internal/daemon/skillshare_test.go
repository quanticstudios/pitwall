package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// TestSkillshareRequest: a window's Skillshare runs on the daemon's
// client and comes back to it alone, steps before the reply, with the
// read trimmed to what the popup parses and an MCP server's secrets
// masked before they leave the daemon. The state says the CLI is there.
func TestSkillshareRequest(t *testing.T) {
	const secret = "synthetic-secret-1234"
	var mu sync.Mutex
	var ran []string
	fake := skillshare.Client{
		Installed: func() bool { return true },
		Run: func(_ context.Context, dir string, args ...string) skillshare.Result {
			mu.Lock()
			ran = append(ran, strings.Join(args, " "))
			mu.Unlock()
			if args[0] == "mcp" {
				return skillshare.Result{Stdout: `{"sourcePath":"/src/mcp.yaml","env":{"TOKEN":"` + secret + `"}}`}
			}
			return skillshare.Result{Stdout: "{}"}
		},
		FS: func(string) ([]byte, error) {
			return []byte("servers:\n  demo:\n    command: npx\n    env:\n      TOKEN: " + secret + "\n"), nil
		},
	}
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) { o.Skillshare = fake })
	defer stop()
	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"})
	c.send(proto.Sync{})
	st := c.waitFor("state", func(m any) bool { _, ok := m.(proto.StateMsg); return ok }).(proto.StateMsg)
	if !st.State.Skillshare {
		t.Fatal("the state does not say skillshare is installed")
	}

	call := func(id uint64, call skillshare.Call) (steps []string, r skillshare.Reply) {
		t.Helper()
		c.send(proto.Skillshare{ID: id, Call: call})
		for {
			m := c.waitFor("skillshare result", func(m any) bool { r, ok := m.(proto.SkillshareResult); return ok && r.ID == id }).(proto.SkillshareResult)
			if m.Done {
				return steps, m.Reply
			}
			steps = append(steps, m.Step)
		}
	}
	_, r := call(1, skillshare.Call{Op: skillshare.OpRead, Name: "mcp", Scope: skillshare.Global})
	if r.Result.Code != 0 || strings.Contains(r.Result.Stdout, secret) || !strings.Contains(r.Result.Stdout, "/src/mcp.yaml") {
		t.Fatalf("read: %+v", r.Result)
	}
	_, r = call(2, skillshare.Call{Op: skillshare.OpPreview, Item: skillshare.Item{Kind: skillshare.MCP, Name: "demo"}, Paths: skillshare.Paths{MCP: "/src/mcp.yaml"}})
	if r.Preview.Err != "" || len(r.Preview.MCP) != 2 || r.Preview.MCP[1].Value != "TOKEN = "+skillshare.Mask {
		t.Fatalf("preview: %+v", r.Preview)
	}
	steps, r := call(3, skillshare.Call{Op: skillshare.OpSync, Scopes: []string{skillshare.Global}})
	if len(steps) == 0 || steps[0] != "Syncing global…" || r.Outcome.Err {
		t.Fatalf("sync: steps %q, outcome %+v", steps, r.Outcome)
	}
	_, r = call(4, skillshare.Call{Op: skillshare.OpRead, Name: "rm -rf", Scope: skillshare.Global})
	mu.Lock()
	last := ran[len(ran)-1]
	mu.Unlock()
	if r.Result.Code == 0 || last != "sync --all --json -g" {
		t.Fatalf("an unknown read ran: %+v, last run %q", r.Result, last)
	}
}

// TestSkillshareMissing: a daemon whose machine has no skillshare says
// so in its state, and a call fails with a reason instead of hanging.
func TestSkillshareMissing(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, _, stop := runDaemon(t, f, func(o *Options) { o.Skillshare = skillshare.Client{} })
	defer stop()
	d.mu.Lock()
	s := d.snapshot()
	d.mu.Unlock()
	if s.Skillshare {
		t.Fatal("no client, but the state says installed")
	}
	var got []any
	d.skillshare(context.Background(), proto.Skillshare{ID: 7}, func(m any) { got = append(got, m) })
	if len(got) != 1 || !got[0].(proto.SkillshareResult).Done || got[0].(proto.SkillshareResult).Reply.Err == "" {
		t.Fatalf("got %+v", got)
	}
}
