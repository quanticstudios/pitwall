package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A Claude pane shows Remote Control while its session's newest process
// file has a bridgeSessionId, and never while the setting is off.
func TestRemoteControl(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	on := true
	o.RemoteControl = func() bool { return on }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: d.st.Workspaces[0].ID}))

	cfg := t.TempDir()
	sessions := filepath.Join(cfg, "sessions")
	must(t, os.MkdirAll(sessions, 0o755))
	write := func(name, body string) { must(t, os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o644)) }
	d.mu.Lock()
	p := &d.st.Panes[0]
	p.Provider, p.SessionID = model.ProviderClaude, "s"
	p.Transcript = filepath.Join(cfg, "projects", "-repo", "s.jsonl")
	d.mu.Unlock()
	got := func() bool {
		d.checkRemoteControl()
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Panes[0].RemoteControl
	}

	write("1.json", `{"sessionId":"s","startedAt":1,"bridgeSessionId":"cse_1"}`)
	write("2.json", `{"sessionId":"other","startedAt":5,"bridgeSessionId":null}`)
	if !got() {
		t.Fatal("bridgeSessionId set, RemoteControl off")
	}
	write("3.json", `{"sessionId":"s","startedAt":2,"bridgeSessionId":null}`)
	if got() {
		t.Fatal("the newest process of the session has no bridge, RemoteControl on")
	}
	write("3.json", `{"sessionId":"s","startedAt":2,"bridgeSessionId":"cse_3"}`)
	on = false
	if got() {
		t.Fatal("setting off, RemoteControl on")
	}
	on = true
	if !got() {
		t.Fatal("setting back on, RemoteControl off")
	}
	d.mu.Lock()
	d.st.Panes[0].Provider = ""
	d.mu.Unlock()
	if got() {
		t.Fatal("agent gone, RemoteControl on")
	}
}
