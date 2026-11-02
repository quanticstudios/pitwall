//go:build unix

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestAgentExplainLive(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_ATTACH", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(sockPath(t, "runtime")))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sock, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	if info, err := os.Stat(sock); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("temp socket %s: %v", sock, err)
	}
	if code, out, stderr := cliOutput("new", "-n", "explain", "-d", t.TempDir(), "--", "sh"); code != 0 {
		t.Fatalf("new: %d %s %s", code, out, stderr)
	}
	c, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := syncCLI(c)
	if err != nil || len(st.Panes) != 1 {
		t.Fatalf("panes: %v, %v", st.Panes, err)
	}
	id := st.Panes[0].ID
	st, err = syncCLI(c, proto.AgentEvent{Pane: id, Provider: model.ProviderCodex,
		Payload: []byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`)})
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := cliOutput("agent", "explain", "@"+id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("explain: %d %s %s", code, out, stderr)
	}
	var r proto.AgentExplanation
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != 1 || r.Pane != id || r.Provider != model.ProviderCodex || r.State != model.StatePendingApproval ||
		r.Rule != "hook" || r.Hook == nil || r.Hook.State != r.State || r.Process == nil || r.Process.Comm == "" {
		t.Fatalf("live: %+v", r)
	}
	if strings.Contains(out, `"checks":null`) {
		t.Fatal("checks must remain a JSON array across the protocol")
	}
	after, err := syncCLI(c)
	if err != nil || !reflect.DeepEqual(st.Panes, after.Panes) || !reflect.DeepEqual(st.Activities, after.Activities) {
		t.Fatalf("explain changed pane or activity: %v", err)
	}
	if code, _, stderr := cliOutput("agent", "explain", "@absent"); code != 1 || stderr == "" {
		t.Fatalf("unknown pane: %d %s", code, stderr)
	}
}
