package daemon

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// TestLogNeverHolds: typed input, hook payloads, command arguments and a
// client's own Kind text never reach the log, on the paths that succeed and the ones that fail, while the
// events around them do.
func TestLogNeverHolds(t *testing.T) {
	var buf lockedBuf
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	const secret = "hunter2-secret"

	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	gui := dial(t, sock, "gui")
	st := gui.waitState("first session", func(s model.State) bool { return len(s.Workspaces) == 1 })
	gui.send(proto.OpenPane{WorkspaceID: st.Workspaces[0].ID, Cmd: []string{"/opt/bin/agent", "--token", secret}})
	st = gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 2 })
	p := st.Panes[1].ID
	gui.send(proto.Input{Pane: p, Data: []byte(secret)})
	gui.send(proto.Input{Pane: "gone", Data: []byte(secret)})
	gui.send(proto.Resize{Pane: p, Cols: 90, Rows: 30})
	odd := dialHello(t, sock, proto.Hello{Version: proto.Version, Kind: "gui\n2026/01/01 forged " + secret})
	odd.send(proto.Sync{})
	odd.waitFor("sync", func(m any) bool { _, ok := m.(proto.StateMsg); return ok })
	hook := dial(t, sock, "hook")
	hook.send(proto.AgentEvent{Pane: p, Provider: model.ProviderClaude, Payload: []byte(secret)})
	hook.send(proto.AgentEvent{Pane: "gone", Provider: model.ProviderClaude, Payload: []byte(secret)})
	waitUntil(t, "input", func() bool { return f.pane(1).got() == secret })
	defer func() {
		if t.Failed() {
			t.Log(buf.String())
		}
	}()
	waitUntil(t, "log lines", func() bool {
		s := buf.String()
		return strings.Contains(s, `proto.AgentEvent: "no pane gone"`) && strings.Contains(s, "resized 80x24 to 90x30")
	})
	stop()
	s := buf.String()
	if strings.Contains(s, secret) || strings.Contains(s, "--token") || strings.Contains(s, "/opt/bin") {
		t.Fatalf("the log holds input, a payload or arguments:\n%s", s)
	}
	if strings.Contains(s, "forged") {
		t.Fatalf("a client's Kind reached the log:\n%s", s)
	}
	for _, want := range []string{`started "agent",`, "other connected", "gui connected", `proto.Input: "no pane gone"`} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in the log:\n%s", want, s)
		}
	}
}
