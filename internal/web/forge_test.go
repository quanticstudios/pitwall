package web

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// fakeForge is fakeChats with a pull request per tab, as the daemon's poll
// keeps them.
type fakeForge struct {
	*fakeChats
	mu      sync.Mutex
	prs     map[string]model.PR
	merged  []string
	prompts []string
}

func (f *fakeForge) PRs() map[string]model.PR {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prs
}

func (f *fakeForge) Reviews(_ context.Context, ws string) ([]forge.Thread, []forge.Comment, error) {
	if ws != "w" {
		return nil, nil, codeErr{"not-found"}
	}
	return []forge.Thread{{Path: "a.go", Line: 3, Comments: []forge.Comment{{Author: "rev", Body: "why?"}}, More: 2}},
		[]forge.Comment{{Author: "rev", Body: "Please split it"}}, nil
}

func (f *fakeForge) Prompt(_ context.Context, ws, what string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, ws+":"+what)
	return "These checks failed on pull request #7:\n- linux\nFix these and push.", nil
}

func (f *fakeForge) Merge(_ context.Context, ws string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.merged) > 0 {
		return codeErr{"forge"}
	}
	f.merged = append(f.merged, ws)
	return nil
}

// setPR replaces tab w's pull request and tells the list's watchers, as
// the daemon's changed does.
func (f *fakeForge) setPR(pr model.PR) {
	f.mu.Lock()
	f.prs = map[string]model.PR{"w": pr}
	f.mu.Unlock()
	f.setList(f.List()...)
}

// TestForge: each chat's list entry carries its tab's pull request, a
// change to it alone is a sessionsChanged, and the three commands answer
// for the chat's tab.
func TestForge(t *testing.T) {
	fc := newFakeChats(t)
	fc.chats = []model.Chat{{ID: "chat-1", WorkspaceID: "w", Provider: model.ProviderClaude, State: "ready"}, {ID: "chat-2", WorkspaceID: "x", Provider: model.ProviderCodex, State: "ready"}}
	f := &fakeForge{fakeChats: fc, prs: map[string]model.PR{"w": {Number: 7, State: model.PROpen, Checks: []model.Check{{Name: "linux", State: model.CheckFail}}}}}
	s, hs := newServer(t, fixed(goodToken))
	s.Chats = f
	c, _ := dial(t, hs, "", "")
	c.send(t, hi(goodToken, Protocol))
	w := c.json(t)
	caps := w["capabilities"].([]any)
	for _, want := range forgeCommands {
		if !slices.Contains(caps, any(want)) {
			t.Fatalf("capabilities %v lack %s", caps, want)
		}
	}
	var v viewer

	c.send(t, map[string]any{"type": "subscribeSessions", "id": "1"})
	expectResult(t, &v, c, "1", true)
	m := v.next(t, c)
	chats := m["chats"].([]any)
	pr := chats[0].(map[string]any)["pr"].(map[string]any)
	if m["type"] != "sessions" || pr["number"] != float64(7) || pr["checks"].([]any)[0].(map[string]any)["state"] != "fail" {
		t.Fatalf("got %v, want chat-1 with #7 failing", m)
	}
	if _, has := chats[1].(map[string]any)["pr"]; has {
		t.Fatalf("chat-2 has a pr: %v", chats[1])
	}

	f.setPR(model.PR{Number: 7, State: model.PROpen, Checks: []model.Check{{Name: "linux", State: model.CheckPass}}})
	m = v.next(t, c)
	changed := m["changed"].([]any)
	if m["type"] != "sessionsChanged" || len(changed) != 1 || changed[0].(map[string]any)["pr"].(map[string]any)["checks"].([]any)[0].(map[string]any)["state"] != "pass" {
		t.Fatalf("got %v, want chat-1's checks passing", m)
	}

	c.send(t, map[string]any{"type": "reviewThreads", "id": "2", "sessionId": "chat-1"})
	data := expectResult(t, &v, c, "2", true)["data"].(map[string]any)
	th := data["threads"].([]any)[0].(map[string]any)
	if th["path"] != "a.go" || th["line"] != float64(3) || th["more"] != float64(2) || data["changeRequests"].([]any)[0].(map[string]any)["body"] != "Please split it" {
		t.Fatalf("got %v", data)
	}
	c.send(t, map[string]any{"type": "reviewThreads", "id": "3", "sessionId": "chat-2"})
	if m := expectResult(t, &v, c, "3", false); m["code"] != "not-found" {
		t.Fatalf("got %v, want not-found for a tab with no pull request", m)
	}

	c.send(t, map[string]any{"type": "prPrompt", "id": "4", "sessionId": "chat-1", "what": "failures"})
	if text := expectResult(t, &v, c, "4", true)["data"].(map[string]any)["text"]; text != "These checks failed on pull request #7:\n- linux\nFix these and push." {
		t.Fatalf("text %q", text)
	}
	c.send(t, map[string]any{"type": "prPrompt", "id": "5", "sessionId": "chat-1", "what": "everything"})
	if m := expectResult(t, &v, c, "5", false); m["code"] != "invalid" {
		t.Fatalf("got %v, want invalid", m)
	}
	c.send(t, map[string]any{"type": "prPrompt", "id": "6", "sessionId": "nope", "what": "review"})
	if m := expectResult(t, &v, c, "6", false); m["code"] != "not-found" {
		t.Fatalf("got %v, want not-found", m)
	}

	c.send(t, map[string]any{"type": "mergePR", "id": "7", "sessionId": "chat-1"})
	expectResult(t, &v, c, "7", true)
	c.send(t, map[string]any{"type": "mergePR", "id": "8", "sessionId": "chat-1"})
	if m := expectResult(t, &v, c, "8", false); m["code"] != "forge" {
		t.Fatalf("got %v, want forge", m)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.merged, []string{"w"}) || !slices.Equal(f.prompts, []string{"w:failures"}) {
		t.Fatalf("merged %v, prompts %v", f.merged, f.prompts)
	}
}

// TestNoForge: a Chats that is no Forge lists no pr and does not offer the
// commands.
func TestNoForge(t *testing.T) {
	s := &Server{Chats: newFakeChats(t)}
	if caps := s.capabilities(); slices.Contains(caps, "mergePR") {
		t.Fatalf("capabilities %v", caps)
	}
	if e := s.entries([]model.Chat{{ID: "a", WorkspaceID: "w"}}); e[0].PR != nil {
		t.Fatalf("entry %+v", e[0])
	}
	if !same(listed{Chat: model.Chat{ID: "a", Seq: 1}}, listed{Chat: model.Chat{ID: "a", Seq: 2}}) {
		t.Fatal("a seq alone changed the entry")
	}
	if same(listed{Chat: model.Chat{ID: "a"}}, listed{Chat: model.Chat{ID: "a"}, PR: &prEntry{Number: 1}}) {
		t.Fatal("a new pull request did not change the entry")
	}
}
