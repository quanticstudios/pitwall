package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestAgyProgram follows the OSC 7501 reports Antigravity CLI 1.5.13 wrote
// in a terminal around a permission prompt and a question: they set the
// agy pane's activity where agy's hooks say nothing.
func TestAgyProgram(t *testing.T) {
	fakeGroups(t)
	d, _, id := openLive(t, 900)
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderAntigravity
	d.mu.Unlock()
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	want := func(step string, state model.AgentState, detail string) {
		t.Helper()
		if a := d.shown(id); a.State != state || a.Detail != detail || state != "" && a.Provider != model.ProviderAntigravity {
			t.Fatalf("%s: %+v, want %q %q", step, a, state, detail)
		}
	}

	status(t, d, id, "state=blocked:kind=auth:app=antigravity:msg="+b64("Sign in required"), 0)
	want("sign-in at start", "", "")
	status(t, d, id, "state=working:app=antigravity", 0)
	want("working with nothing asked: the hooks' to say", "", "")
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64("Approve Bash: rm -rf ./junk"), 0)
	want("permission prompt", model.StatePendingApproval, "Approve Bash: rm -rf ./junk")
	status(t, d, id, "state=working:app=antigravity", 0)
	want("approved", model.StateWorking, "")
	status(t, d, id, "state=done:app=antigravity:msg="+b64("Removed it."), 0)
	want("done while working stays with the Stop hook", model.StateWorking, "")
	status(t, d, id, "state=blocked:kind=question:app=antigravity:msg="+b64("Which color do you like?"), 0)
	want("question", model.StateAwaitingInput, "Which color do you like?")
	status(t, d, id, "state=done:app=antigravity:msg="+b64("You picked **red**."), 0)
	want("answered and done", model.StateCompleted, "You picked **red**.")
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64("Approve Bash: rmdir ./j2"), 0)
	status(t, d, id, "state=idle:app=antigravity", 0)
	want("denied with Esc, which interrupts the turn", "", "")

	// Another program's report, or one under another agent, is no agy's.
	status(t, d, id, "state=blocked:kind=permission:app=make", 0)
	want("another app", "", "")
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderClaude
	d.mu.Unlock()
	status(t, d, id, "state=blocked:kind=permission:app=antigravity", 0)
	if a := d.shown(id); a.Provider == model.ProviderAntigravity {
		t.Fatalf("an agy report under Claude Code set %+v", a)
	}
}

// TestAgyTrustQuestion: agy's folder-trust question makes the tab need
// the user, and a hook from the prompt agy runs meanwhile in its scratch
// folder does not mark it Done.
func TestAgyTrustQuestion(t *testing.T) {
	fakeGroups(t)
	d, _, id := openLive(t, 900)
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderAntigravity
	d.mu.Unlock()
	b64 := base64.StdEncoding.EncodeToString([]byte("Trust this folder?"))
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64, 0)
	if a := d.shown(id); a.State != model.StateAwaitingInput || a.Detail != "Trust this folder?" {
		t.Fatalf("trust question: %+v", a)
	}
	for _, hook := range []string{
		`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"fix it"}`,
		`{"hook_event_name":"Stop","session_id":"s","last_assistant_message":"Done in scratch."}`,
	} {
		must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: []byte(hook)}))
		if a := d.shown(id); a.State != model.StateAwaitingInput {
			t.Fatalf("after %s: %+v", hook, a)
		}
	}
	// Trusted: agy reports idle, and its hooks own the tab again.
	status(t, d, id, "state=idle:app=antigravity", 0)
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"go"}`)}))
	if a := d.shown(id); a.State == model.StateAwaitingInput || a.State == "" {
		t.Fatalf("after trusting, the hook changed nothing: %+v", a)
	}
}

// TestAgyTrustWorktree: a worktree of a repo agy trusts is trusted before
// agy starts in it, in agy's own file and mode; one of a repo it does not
// trust is not.
func TestAgyTrustWorktree(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "fix")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "fix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "fix")+"\n"), 0o644)
	env := []string{"HOME=" + home}
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if note := agyTrustWorktree(wt, env); note != "" {
		t.Fatalf("no settings yet, yet: %s", note)
	}
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(`{"trustedWorkspaces":["/elsewhere"]}`), 0o600)
	if note := agyTrustWorktree(wt, env); note != "" {
		t.Fatalf("an untrusted repo's worktree: %s", note)
	}
	os.WriteFile(settings, []byte(`{"trustedWorkspaces":["`+repo+`"]}`), 0o600)
	if note := agyTrustWorktree(wt, env); note == "" {
		t.Fatal("a trusted repo's worktree was not trusted")
	}
	b, _ := os.ReadFile(settings)
	var s struct{ TrustedWorkspaces []string }
	if json.Unmarshal(b, &s) != nil || !slices.Equal(s.TrustedWorkspaces, []string{repo, wt}) {
		t.Fatalf("settings %s", b)
	}
	if info, _ := os.Stat(settings); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if note := agyTrustWorktree(repo, env); note != "" {
		t.Errorf("the main repo itself: %s", note)
	}
}
