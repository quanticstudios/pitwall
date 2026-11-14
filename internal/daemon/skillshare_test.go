package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// Both an explicit cancel and a lost connection stop daemon-side work.
func TestSkillshareCancellation(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disconnect"}[disconnect], func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			fake := skillshare.Client{Run: func(ctx context.Context, _ string, _ ...string) skillshare.Result {
				close(started)
				<-ctx.Done()
				close(stopped)
				return skillshare.Result{Code: -1}
			}}
			f := &fakes{statsCalls: map[string]int{}}
			sock, stop := runWith(t, f, func(o *Options) { o.Skillshare = fake })
			defer stop()
			c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"})
			c.send(proto.Skillshare{ID: 1, Call: skillshare.Call{Op: skillshare.OpRead, Name: "list", Scope: skillshare.Global}})
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("call did not start")
			}
			if disconnect {
				c.conn.Close()
			} else {
				c.send(proto.Skillshare{ID: 1, Cancel: true})
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon kept the canceled operation running")
			}
		})
	}
}

func TestSkillshareCloneOwnership(t *testing.T) {
	for _, phase := range []string{"during-discovery", "after-discovery", "disconnect", "during-audit"} {
		t.Run(phase, func(t *testing.T) {
			cloned, release := make(chan string, 1), make(chan struct{})
			auditing, releaseAudit := make(chan struct{}), make(chan struct{})
			d := &Daemon{o: Options{Skillshare: skillshare.Client{
				Clone: func(_ context.Context, _, _, dest string) error {
					cloned <- filepath.Dir(dest)
					<-release // model a success racing with cancellation
					return nil
				},
				Run: func(_ context.Context, _ string, args ...string) skillshare.Result {
					if args[0] == "audit" {
						close(auditing)
						<-releaseAudit
						return skillshare.Result{Stdout: `{"results":[]}`}
					}
					return skillshare.Result{Stdout: `{"planned":[{"name":"check","path":"check"}]}`}
				},
			}}}
			var calls skillshareCalls
			defer calls.close()
			results := make(chan proto.SkillshareResult, 8)
			send := func(m any) { results <- m.(proto.SkillshareResult) }
			r := proto.Skillshare{ID: 1, Call: skillshare.Call{Op: skillshare.OpDiscover, Source: "acme/skills", Scope: skillshare.Global}}
			calls.request(context.Background(), d, r, send)
			dir := <-cloned
			if phase == "during-discovery" {
				calls.request(context.Background(), d, r, send) // duplicate must not orphan the first call
				calls.request(context.Background(), d, proto.Skillshare{ID: 1, Cancel: true}, send)
			}
			close(release)
			select {
			case <-results:
			case <-time.After(5 * time.Second):
				t.Fatal("discovery did not return")
			}
			switch phase {
			case "after-discovery":
				calls.request(context.Background(), d, proto.Skillshare{ID: 1, Cancel: true}, send)
			case "disconnect":
				calls.close()
			case "during-audit":
				calls.request(context.Background(), d, proto.Skillshare{ID: 2, Call: skillshare.Call{Op: skillshare.OpAudit, Clone: dir, Picks: []skillshare.Choice{{Path: "check"}}}}, send)
				<-auditing
				calls.request(context.Background(), d, proto.Skillshare{ID: 3, Call: skillshare.Call{Op: skillshare.OpDrop, Clone: dir}}, send)
				<-results // drop's reply
				if _, err := os.Stat(dir); err != nil {
					t.Fatal("clone removed while audit still uses it")
				}
				close(releaseAudit)
				<-results
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("clone survived %s: %v", phase, err)
			}
		})
	}
}

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
