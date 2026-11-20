package daemon

import (
	"encoding/base64"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
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
