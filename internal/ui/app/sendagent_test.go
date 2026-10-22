package app

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// waitSends waits for u's fetches to answer, then hands them to takeSends.
func waitSends(t *testing.T, u *ui, st *model.State) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		u.sends.mu.Lock()
		n := len(u.sends.done)
		u.sends.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the fetch never answered")
		}
	}
	var r input.Router
	var ops op.Ops
	u.takeSends(gl.Context{Ops: &ops, Source: r.Source(), Now: time.Now(), Constraints: gl.Exact(image.Pt(800, 600))}, st)
}

// TestSendToAgent: Send failures pastes the fetched checks into the tab's
// agent, nothing to send says so, and a gh error lands in the notice box.
func TestSendToAgent(t *testing.T) {
	oldGH, oldF, oldR := ghInstalled, fetchFailures, fetchReviews
	t.Cleanup(func() { ghInstalled, fetchFailures, fetchReviews = oldGH, oldF, oldR })
	ghInstalled = func() bool { return true }
	fetchFailures = func(_ context.Context, dir string) ([]forge.FailedCheck, error) {
		if dir != "/repo" {
			t.Errorf("fetched in %q", dir)
		}
		return []forge.FailedCheck{{Name: "test", URL: "https://example.invalid/job/1", Log: "--- FAIL: TestWidget"}}, nil
	}
	fetchReviews = func(context.Context, string, int) ([]forge.Thread, []forge.Comment, error) {
		return nil, nil, nil
	}
	b := newAgentBackend(model.StateCompleted)
	b.st.PRs = map[string]model.PR{"w": {Number: 12, State: model.PROpen, Checks: []model.Check{{Name: "test", State: model.CheckFail}}}}
	u := &ui{b: b}
	st := b.State()

	u.sidebarEvent(&st, sidebar.SendFailures{WorkspaceID: "w"})
	waitSends(t, u, &st)
	if len(b.sent) != 1 || b.sent[0].Pane != "p" {
		t.Fatalf("sent %q", b.sent)
	}
	paste := string(b.sent[0].Data)
	if !strings.HasPrefix(paste, "\x1b[200~These checks failed on pull request #12:") || !strings.Contains(paste, "--- FAIL: TestWidget") ||
		!strings.HasSuffix(paste, "Fix these and push.\x1b[201~") {
		t.Errorf("paste %q", paste)
	}
	if u.notice != "Sent to claude" {
		t.Errorf("notice %q", u.notice)
	}

	u.sidebarEvent(&st, sidebar.SendReview{WorkspaceID: "w"})
	waitSends(t, u, &st)
	if u.notice != "No unresolved review comments" || len(u.review.queue) != 0 {
		t.Errorf("nothing to send: notice %q, queue %d", u.notice, len(u.review.queue))
	}

	fetchReviews = func(context.Context, string, int) ([]forge.Thread, []forge.Comment, error) {
		return nil, nil, errors.New("gh api: HTTP 502")
	}
	u.sidebarEvent(&st, sidebar.SendReview{WorkspaceID: "w"})
	waitSends(t, u, &st)
	if u.sends.err != "Send review comments to agent: gh api: HTTP 502" {
		t.Errorf("error %q", u.sends.err)
	}

	// Closed, the same failure shows again the next time.
	u.dismissed = u.sends.err
	u.sends.err = ""
	u.sidebarEvent(&st, sidebar.SendReview{WorkspaceID: "w"})
	waitSends(t, u, &st)
	if u.sends.err == "" || u.dismissed == u.sends.err {
		t.Errorf("a repeated error stays hidden: err %q, dismissed %q", u.sends.err, u.dismissed)
	}

	// A busy agent holds one send; another waits for none of its own.
	b.st.Activities[0].State = model.StateWorking
	st = b.State()
	sent := len(b.sent)
	for range 2 {
		u.sidebarEvent(&st, sidebar.SendFailures{WorkspaceID: "w"})
		waitSends(t, u, &st)
	}
	if len(u.review.queue) != 1 || strings.Count(u.review.queue[0].text, "These checks failed") != 1 || len(b.sent) != sent ||
		!strings.HasSuffix(u.notice, "this one was dropped") {
		t.Errorf("busy agent: queue %d, notice %q", len(u.review.queue), u.notice)
	}

	b.st.Panes[0].Provider = model.ProviderTerminal
	b.st.Activities = nil
	st = b.State()
	if f, r := sendBlocked(&st, st.Workspaces[0]); f != "no agent" || r != "no agent" {
		t.Errorf("without an agent: %q %q", f, r)
	}
}
