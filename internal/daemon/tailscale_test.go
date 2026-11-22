package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/remote"
)

type fakeTailscaleNode struct {
	mu          sync.Mutex
	allowUsers  []string
	allowTags   []string
	policyCalls int
	stopped     bool
	loggedOut   bool
}

func (f *fakeTailscaleNode) UpdatePolicy(users, tags []string) {
	f.mu.Lock()
	f.allowUsers = slices.Clone(users)
	f.allowTags = slices.Clone(tags)
	f.policyCalls++
	f.mu.Unlock()
}

func (f *fakeTailscaleNode) Stop(logout bool) error {
	f.mu.Lock()
	f.stopped = true
	f.loggedOut = logout
	f.mu.Unlock()
	return nil
}

func TestDaemonTailscaleLifecycle(t *testing.T) {
	remoteDir := t.TempDir()
	var (
		mu    sync.Mutex
		cfg   = config.RemoteSettings{Listen: config.DefaultRemoteListen, Tailscale: config.RemoteTailscaleSettings{Enabled: false, Hostname: "pitwall-box"}}
		nodes []*fakeTailscaleNode
	)
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RemoteDir = remoteDir
	o.Remote = func() config.RemoteSettings {
		mu.Lock()
		defer mu.Unlock()
		return cfg
	}
	o.TailscaleCheck = 10 * time.Millisecond
	o.TailscaleStart = func(c tsnetStartConfig) (tailscaleNode, error) {
		n := &fakeTailscaleNode{
			allowUsers: slices.Clone(c.Config.AllowUsers),
			allowTags:  slices.Clone(c.Config.AllowTags),
		}
		if err := os.MkdirAll(c.Config.Dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(c.Config.Dir, "tailscaled.state"), []byte("{}"), 0o600); err != nil {
			return nil, err
		}
		if c.Config.OnStatus != nil {
			c.Config.OnStatus(remote.TailscaleStatus{
				Enabled:  true,
				State:    remote.TailscaleRunning,
				Hostname: c.Config.Hostname,
				URL:      "https://" + c.Config.Hostname + ".tail1.ts.net/",
			})
		}
		mu.Lock()
		nodes = append(nodes, n)
		mu.Unlock()
		return n, nil
	}

	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.tailscaleLoop(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitFor := func(desc string, pred func() bool) {
		t.Helper()
		for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(10 * time.Millisecond) {
			if pred() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", desc)
	}

	// Off by default: never starts a node.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	if len(nodes) != 0 {
		t.Fatalf("started node while disabled: %d", len(nodes))
	}
	cfg.Tailscale.Enabled = true
	mu.Unlock()

	// Opt-in starts the node and writes running status.
	waitFor("node start", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(nodes) == 1
	})
	st, ok := remote.ReadTailscaleStatus(remoteDir)
	if !ok || st.State != remote.TailscaleRunning || st.Hostname != "pitwall-box" {
		t.Fatalf("status after opt-in: %+v ok=%v", st, ok)
	}

	// Updating allow_users updates live policy without restarting the node.
	mu.Lock()
	cfg.Tailscale.AllowUsers = []string{"bob@example.com"}
	mu.Unlock()
	waitFor("live policy update", func() bool {
		nodes[0].mu.Lock()
		defer nodes[0].mu.Unlock()
		return nodes[0].policyCalls == 1 && slices.Equal(nodes[0].allowUsers, []string{"bob@example.com"})
	})
	mu.Lock()
	if len(nodes) != 1 {
		t.Fatalf("policy update restarted node: %d", len(nodes))
	}
	// Turning off with purge requested logs out cleanly and deletes state.
	if err := remote.RequestTailscalePurge(remoteDir); err != nil {
		t.Fatal(err)
	}
	cfg.Tailscale.Enabled = false
	mu.Unlock()

	waitFor("logout and state removal", func() bool {
		nodes[0].mu.Lock()
		stopped, loggedOut := nodes[0].stopped, nodes[0].loggedOut
		nodes[0].mu.Unlock()
		return stopped && loggedOut && !remote.TailscaleStateExists(remoteDir)
	})

	// Turn back on, then shut down daemon ctx -> must NOT log out!
	mu.Lock()
	cfg.Tailscale.Enabled = true
	mu.Unlock()
	waitFor("second start", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(nodes) == 2
	})
	cancel()
	<-done
	nodes[1].mu.Lock()
	stopped, loggedOut := nodes[1].stopped, nodes[1].loggedOut
	nodes[1].mu.Unlock()
	if !stopped || loggedOut {
		t.Fatalf("daemon shutdown: stopped=%v loggedOut=%v (want stopped=true, loggedOut=false)", stopped, loggedOut)
	}
	if !remote.TailscaleStateExists(remoteDir) {
		t.Fatal("daemon shutdown removed tailscale state")
	}
}
