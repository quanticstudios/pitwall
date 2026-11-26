package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

func agyScreenFixture(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func agyPaneDaemon(t *testing.T, state model.AgentState, screen []string) (*Daemon, *fakePane) {
	t.Helper()
	d, f, _ := remoteDaemon(t)
	oldQuiet, oldSettle, oldLook, oldReply := sendQuiet.set(time.Millisecond), sendSettle.set(time.Millisecond), sendLook.set(20*time.Millisecond), replyDelay.set(20*time.Millisecond)
	t.Cleanup(func() {
		d.mu.Lock()
		for _, w := range d.mail.wake {
			w.Stop()
		}
		clear(d.mail.queue)
		d.mu.Unlock()
		d.sendMu.Lock()
		sendQuiet.set(oldQuiet)
		sendSettle.set(oldSettle)
		sendLook.set(oldLook)
		sendReply := oldReply
		replyDelay.set(sendReply)
		d.sendMu.Unlock()
	})
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderAntigravity
	a := &d.st.Activities[d.activityIndex("a")]
	a.Provider, a.State, a.Detail, a.UpdatedAt = model.ProviderAntigravity, state, "", time.Now()
	d.mu.Unlock()
	p := f.pane(0)
	setScreen(p, screen)
	return d, p
}

// A send while Antigravity is merely working ("esc to cancel" at the bottom,
// including after printing a numbered list) goes straight in with Alt+M and
// is never mistaken for a form.
func TestAgyPaneSendWhileWorking(t *testing.T) {
	for _, fixture := range []string{
		"antigravity-working.txt",
		"antigravity-working-list.txt",
		"antigravity-working-generating.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			d, p := agyPaneDaemon(t, model.StateWorking, agyScreenFixture(t, fixture))
			r, ok := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "add a test", How: "steer"})
			if !ok {
				t.Fatal("chatRequest did not handle pane chat")
			}
			res := r.(proto.ChatResult)
			if res.Code != "" || res.Message != "" {
				t.Fatalf("working agy send: %+v", res)
			}
			want := "add a test\x1bm"
			if got := gotAfter(t, p, 0, len(want)); got != want {
				t.Fatalf("typed %q, want %q", got, want)
			}
			snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
			if len(snap.QueuedInputs) != 0 {
				t.Fatalf("queue left non-empty: %+v", snap.QueuedInputs)
			}
		})
	}
}

// A send while a real Antigravity form or menu is already open is never typed
// into the terminal: it stays visible in the chat queue with the hold reason
// and goes automatically once the menu closes.
func TestAgyPaneSendHeldBeforeForm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   model.AgentState
		fixture string
		wantWhy string
	}{
		{"model menu while working", model.StateWorking, "antigravity-model-menu.txt", waitForm},
		{"settings menu while idle", model.StateCompleted, "antigravity-settings-menu.txt", waitForm},
		{"permission prompt while working", model.StateWorking, "antigravity-approval.txt", waitForm},
		{"question while working", model.StateWorking, "antigravity-question.txt", waitForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, p := agyPaneDaemon(t, tc.state, agyScreenFixture(t, tc.fixture))
			how := "steer"
			if tc.state == model.StateCompleted {
				how = ""
			}
			r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "hold this", How: how})
			if res := r.(proto.ChatResult); res.Code != "" || res.Message != "queued" {
				t.Fatalf("send over form: %+v, want queued", res)
			}
			time.Sleep(60 * time.Millisecond)
			if got := p.got(); got != "" {
				t.Fatalf("typed %q while form was up", got)
			}
			snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
			if len(snap.QueuedInputs) != 1 || snap.QueuedInputs[0].Text != "hold this" || snap.QueuedInputs[0].Error != "" {
				t.Fatalf("queued rows: %+v", snap.QueuedInputs)
			}
			if snap.QueueReason != tc.wantWhy {
				t.Fatalf("queue reason = %q, want %q", snap.QueueReason, tc.wantWhy)
			}
			// Closing the menu lets the held row go automatically.
			if tc.state == model.StateWorking {
				setScreen(p, agyScreenFixture(t, "antigravity-working.txt"))
				want := "hold this\x1bm"
				if got := gotAfter(t, p, 0, len(want)); got != want {
					t.Fatalf("after menu closed: typed %q, want %q", got, want)
				}
			} else {
				setScreen(p, agyScreenFixture(t, "antigravity-idle.txt"))
				want := "hold this\r"
				if got := gotAfter(t, p, 0, len(want)); got != want {
					t.Fatalf("after menu closed: typed %q, want %q", got, want)
				}
			}
			waitUntil(t, "held row cleared after sending", func() bool {
				s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
				return len(s.QueuedInputs) == 0 && s.QueueReason == ""
			})
		})
	}
}

// When a real form pops up in the window between pasting and pressEnter,
// pressEnter refuses Enter, clears the typed text with Ctrl+U, drops the
// prompt echo, and returns the message to the chat as a held queued row
// with waitForm until the form closes.
func TestAgyPaneSendFormAppearsMidPaste(t *testing.T) {
	d, p := agyPaneDaemon(t, model.StateWorking, agyScreenFixture(t, "antigravity-working.txt"))
	menu := agyScreenFixture(t, "antigravity-model-menu.txt")
	p.mu.Lock()
	flipped := false
	p.wrote = func() {
		if !flipped {
			flipped = true
			p.screen = menu
		}
	}
	p.mu.Unlock()

	r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "mid-paste", How: "steer"})
	if res := r.(proto.ChatResult); res.Code != "" {
		t.Fatalf("send: %+v", res)
	}
	// Typed text is cleared with Ctrl+U and no Enter/Alt+M is pressed.
	wantCleared := "mid-paste\x15"
	if got := gotAfter(t, p, 0, len(wantCleared)); got != wantCleared {
		t.Fatalf("typed %q, want %q", got, wantCleared)
	}
	waitUntil(t, "returned to queue as held row", func() bool {
		s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
		return len(s.QueuedInputs) == 1 && s.QueuedInputs[0].Text == "mid-paste" &&
			s.QueuedInputs[0].Error == "" && s.QueueReason == waitForm
	})
	snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
	for _, it := range snap.Live {
		if it.ID == livePrompt {
			t.Fatalf("stale prompt echo remained in Live: %+v", it)
		}
	}
	// Once the menu closes, the message goes in automatically.
	before := len(p.got())
	setScreen(p, agyScreenFixture(t, "antigravity-working.txt"))
	wantSent := "mid-paste\x1bm"
	if got := gotAfter(t, p, before, len(wantSent)); got != wantSent {
		t.Fatalf("after menu closed: typed %q, want %q", got, wantSent)
	}
	waitUntil(t, "queue drained", func() bool {
		s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
		return len(s.QueuedInputs) == 0
	})
}

// When an immediate pane-chat send fails to write to the pane (for any
// non-form reason), the message is not silently lost: it comes back into the
// chat queue with an Error so the user sees Retry.
func TestPaneSendFailureReturnsFailedRowWithRetry(t *testing.T) {
	d, p := agyPaneDaemon(t, model.StateCompleted, agyScreenFixture(t, "antigravity-idle.txt"))
	// Make the pane's input buffer block/fail once deliver starts writing.
	p.mu.Lock()
	p.wrote = func() {
		d.mu.Lock()
		d.inputs["a"] = nil
		d.mu.Unlock()
	}
	p.mu.Unlock()

	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "do not lose me"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("send: %+v", r)
	}
	waitUntil(t, "failed row visible with Retry", func() bool {
		s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
		return len(s.QueuedInputs) == 1 && s.QueuedInputs[0].Text == "do not lose me" && s.QueuedInputs[0].Error != ""
	})
	// Clicking Retry / Send now on a failed row when writing fails again keeps
	// the row failed in the queue instead of dropping it.
	snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
	id := snap.QueuedInputs[0].ID
	d.mu.Lock()
	d.inputs["a"] = make(chan []byte, 1) // accepts the paste, then fails on Enter when wrote nils it
	d.mu.Unlock()
	if err := d.paneQueue("a", proto.ChatQueue{Action: "now", ID: id}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitUntil(t, "retried row still visible with Retry", func() bool {
		s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
		return len(s.QueuedInputs) == 1 && s.QueuedInputs[0].Text == "do not lose me" && s.QueuedInputs[0].Error != ""
	})
}

// A non-chat message that fails to write shows a notice instead of only
// logging that a message was lost.
func TestNonChatSendFailureSetsNotice(t *testing.T) {
	d, f := talkDaemon(t)
	oldQuiet, oldSettle, oldReply := sendQuiet.set(time.Millisecond), sendSettle.set(time.Millisecond), replyDelay.set(10*time.Millisecond)
	t.Cleanup(func() {
		d.mu.Lock()
		for _, w := range d.mail.wake {
			w.Stop()
		}
		clear(d.mail.queue)
		d.mu.Unlock()
		d.sendMu.Lock()
		sendQuiet.set(oldQuiet)
		sendSettle.set(oldSettle)
		replyDelay.set(oldReply)
		d.sendMu.Unlock()
	})
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderPi
	d.mu.Unlock()
	p := f.pane(0)
	setScreen(p, []string{"> "})
	p.mu.Lock()
	p.wrote = func() {
		d.mu.Lock()
		d.inputs["a"] = nil
		d.mu.Unlock()
	}
	p.mu.Unlock()
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "notice me"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "notice shown on send failure", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return strings.HasPrefix(d.st.Notice, "Message not sent: ")
	})
}

// Antigravity's TUI ignores Enter on an image with no text, so an
// image-only send types "(image)" after the path to go.
func TestAgyTypeLetterImageOnly(t *testing.T) {
	d, p := agyPaneDaemon(t, model.StateCompleted, agyScreenFixture(t, "antigravity-idle.txt"))
	if err := d.typeLetter("a", p, []string{"/tmp/a.png"}, "", nil); err != nil {
		t.Fatal(err)
	}
	want := "'/tmp/a.png'(image)\r"
	if got := gotAfter(t, p, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}
