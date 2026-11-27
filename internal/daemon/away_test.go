package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

const awaySecret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

// awaySnap is a chat whose user left at left: a prompt and a reply from
// before, then a turn the user missed with edits, commands, a failed
// test run and a reply.
func awaySnap(left time.Time) session.Snapshot {
	before, after := session.Stamp(left.Add(-time.Minute)), session.Stamp(left.Add(time.Minute))
	t1, t2 := "t1", "t2"
	one, zero := 1, 0
	edit := func(id, path string) session.Item {
		in, _ := json.Marshal(map[string]string{"file_path": path})
		return session.Item{ID: id, Kind: session.KindToolCall, CreatedAt: after, TurnID: &t2,
			Tool: &session.ToolCall{ToolName: "Edit", Status: "completed", Input: in}}
	}
	run := func(id, cmd string, code *int) session.Item {
		status := "completed"
		if code != nil && *code != 0 {
			status = "failed"
		}
		return session.Item{ID: id, Kind: session.KindToolCall, CreatedAt: after, TurnID: &t2,
			Tool: &session.ToolCall{ToolName: "Bash", Command: cmd, Status: status, ExitCode: code}}
	}
	return session.Snapshot{Content: []session.Item{
		{ID: "u1", Kind: session.KindUserPrompt, Text: "Fix the login race", CreatedAt: before, TurnID: &t1},
		{ID: "a1", Kind: session.KindAssistantText, Text: "OLD REPLY", CreatedAt: before, TurnID: &t1},
		{ID: "u2", Kind: session.KindUserPrompt, Text: "Now add a test, token " + awaySecret, CreatedAt: before, TurnID: &t2},
		edit("e1", "/repo/auth/login.go"),
		edit("e2", "/repo/auth/login_test.go"),
		edit("e3", "/repo/auth/login.go"), // the same file again counts once
		run("c1", "go build ./...", &zero),
		run("c2", "GH_TOKEN="+awaySecret+" go test ./auth/...\nmore", &one),
		{ID: "a2", Kind: session.KindAssistantText, Text: "The new test fails: the cookie expires early.", CreatedAt: after, TurnID: &t2},
	}}
}

func TestAwayFacts(t *testing.T) {
	left := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	a, ok := awayFacts(awaySnap(left), left)
	if !ok {
		t.Fatal("no card for a turn the user missed")
	}
	if a.Item != "e1" || a.Seq != 4 || !a.Since.Equal(left) {
		t.Errorf("first missed: %q seq %d since %v", a.Item, a.Seq, a.Since)
	}
	if a.Replies != 1 || a.Turns != 1 || a.Commands != 2 || a.Tests != "failed" {
		t.Errorf("counts: %+v", a)
	}
	if a.EditedN != 2 || strings.Join(a.Edited, ",") != "login.go,login_test.go" {
		t.Errorf("edited: %d %q", a.EditedN, a.Edited)
	}
	if !strings.Contains(a.Failed, "go test ./auth/...") || strings.Contains(a.Failed, awaySecret) || strings.Contains(a.Failed, "more") {
		t.Errorf("failed: %q", a.Failed)
	}
	if got := strings.Join(model.AwayFacts(a), " · "); got != "1 message · 2 files edited · 2 commands · tests failed" {
		t.Errorf("facts line %q", got)
	}

	// Nothing since, or only the user's own words: no card.
	if _, ok := awayFacts(awaySnap(left), left.Add(time.Hour)); ok {
		t.Error("a card with nothing new")
	}
	s := awaySnap(left)
	s.Content = append(s.Content[:3:3], session.Item{ID: "u3", Kind: session.KindUserPrompt, Text: "hi", CreatedAt: session.Stamp(left.Add(time.Minute))})
	if _, ok := awayFacts(s, left); ok {
		t.Error("a card for the user's own prompt")
	}
}

func TestAwayPrompt(t *testing.T) {
	left := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	p := awayPrompt(awaySnap(left), left)
	for _, want := range []string{"Now add a test", "The new test fails", "State:", "Needs:"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, awaySecret) || strings.Contains(p, "OLD REPLY") || strings.Contains(p, "go test") {
		t.Errorf("prompt has a secret, a reply from before or a command:\n%s", p)
	}

	// The caps: the message's start, the replies' end.
	s := awaySnap(left)
	s.Content[2].Text = "START" + strings.Repeat("u", 3000) + "USEREND"
	s.Content[len(s.Content)-1].Text = "REPLYSTART" + strings.Repeat("r", 10000) + "END"
	p = awayPrompt(s, left)
	user := between(p, "<user_message>\n", "\n</user_message>")
	reply := between(p, "<agent_replies>\n", "\n</agent_replies>")
	if n := len([]rune(user)); n != awayUserRunes+1 || !strings.HasPrefix(user, "START") || strings.Contains(user, "USEREND") {
		t.Errorf("user message: %d runes", n)
	}
	if n := len([]rune(reply)); n != awayReplyRunes+1 || !strings.HasSuffix(reply, "END") || strings.Contains(reply, "REPLYSTART") {
		t.Errorf("replies: %d runes", n)
	}

	s.Content = s.Content[:len(s.Content)-1]
	if p := awayPrompt(s, left); p != "" {
		t.Errorf("a prompt with no reply since: %q", p)
	}
}

func between(s, a, b string) string {
	_, s, _ = strings.Cut(s, a)
	s, _, _ = strings.Cut(s, b)
	return s
}

func TestAwayLines(t *testing.T) {
	for _, tc := range []struct{ in, status, needs string }{
		{"State: The fix is in; one test fails.\nNeeds: Decide on the cookie lifetime.", "The fix is in; one test fails.", "Decide on the cookie lifetime."},
		{"**State:** Done.\n\n**Needs:** Nothing", "Done.", "Nothing"},
		{"Work is done\nReview it", "Work is done", "Review it"},
		{"", "", ""},
	} {
		s, n := awayLines(tc.in)
		if s != tc.status || n != tc.needs {
			t.Errorf("awayLines(%q) = %q, %q", tc.in, s, n)
		}
	}
}

func TestAwayFor(t *testing.T) {
	for d, want := range map[time.Duration]string{7 * time.Minute: "7m", 2*time.Hour + 14*time.Minute: "2h 14m", 3 * time.Hour: "3h", 50 * time.Hour: "2d"} {
		if got := model.AwayFor(d); got != want {
			t.Errorf("AwayFor(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestAwayReturn: a chat out of sight for AwayAfter that its agent worked
// in gets a card on the return, with one summary call however often the
// state is read, and loses it on a send; a short absence gets none.
func TestAwayReturn(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	left := time.Now().Add(-10 * time.Minute)
	suggestPane(t, d, "a", awaySnap(left))
	var summary atomic.Bool
	summary.Store(true)
	d.o.AwaySummary = summary.Load
	var calls atomic.Int32
	var gotPrompt atomic.Value
	old := awaySummarize
	awaySummarize = func(_ context.Context, _ suggestRun, prompt string) (string, error) {
		calls.Add(1)
		gotPrompt.Store(prompt)
		return "State: One test fails.\nNeeds: Pick a cookie lifetime.", nil
	}
	t.Cleanup(func() { awaySummarize = old })

	card := func() (model.Away, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		a, ok := d.snapshot().Away["a"]
		return a, ok
	}
	wait := func(done func(model.Away, bool) bool) model.Away {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			if a, ok := card(); done(a, ok) {
				return a
			}
		}
		a, ok := card()
		t.Fatalf("card %+v (%v) never came", a, ok)
		return a
	}
	// leave shows the chat on a window, then takes it away with the last
	// sight back dated by ago.
	c := &client{}
	show := func(on bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if on {
			d.clients[c] = struct{}{}
			c.focus = "a"
		} else {
			c.focus = ""
		}
		d.awayLook("a")
	}
	leave := func(ago time.Duration) {
		show(true)
		show(false)
		d.mu.Lock()
		d.away.seen["a"] = time.Now().Add(-ago)
		d.mu.Unlock()
	}
	t.Cleanup(func() { d.mu.Lock(); delete(d.clients, c); d.mu.Unlock() })

	leave(2 * time.Minute)
	show(true)
	time.Sleep(50 * time.Millisecond)
	if _, ok := card(); ok {
		t.Fatal("a card after 2 minutes away")
	}

	show(false)
	d.mu.Lock()
	d.away.seen["a"] = left
	d.mu.Unlock()
	show(true)
	a := wait(func(a model.Away, ok bool) bool { return ok && !a.Asking })
	if a.Status != "One test fails." || a.Needs != "Pick a cookie lifetime." || a.Item != "e1" || a.Replies != 1 {
		t.Fatalf("card %+v", a)
	}
	if p, _ := gotPrompt.Load().(string); strings.Contains(p, awaySecret) {
		t.Error("the summary prompt carries a secret")
	}
	for range 5 {
		card()
		show(false)
		show(true) // back at once: no new absence
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d summary calls for one return", n)
	}

	d.mu.Lock()
	d.awaySeen("a") // a send
	d.mu.Unlock()
	if _, ok := card(); ok {
		t.Fatal("the card stayed after a send")
	}

	// Off: the facts alone, no call; a phone opening the chat counts as a return.
	summary.Store(false)
	show(false)
	d.mu.Lock()
	d.away.seen["a"] = left
	d.mu.Unlock()
	d.awayPhone("a", 1)
	d.awayView("a", true) // the app in the foreground beats
	a = wait(func(a model.Away, ok bool) bool { return ok })
	if a.Asking || a.Status != "" || a.Commands != 2 {
		t.Fatalf("card with summaries off: %+v", a)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("summaries off still called the model (%d calls)", n)
	}
	d.mu.Lock()
	d.awayDrop("a") // dismissed
	d.mu.Unlock()
	if _, ok := card(); ok {
		t.Fatal("the card stayed after a dismiss")
	}
	d.awayPhone("a", -1)
}

// TestAwaySummaryFails: a failed call leaves the card with its facts and
// no summary.
func TestAwaySummaryFails(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	left := time.Now().Add(-time.Hour)
	suggestPane(t, d, "a", awaySnap(left))
	d.o.AwaySummary = func() bool { return true }
	old := awaySummarize
	awaySummarize = func(context.Context, suggestRun, string) (string, error) {
		return "State: x", context.DeadlineExceeded
	}
	t.Cleanup(func() { awaySummarize = old })
	go d.awayOpen("a", left, time.Now())
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		a, ok := d.st.Away["a"]
		d.mu.Unlock()
		if ok && !a.Asking {
			if a.Status != "" || a.Needs != "" || a.Commands != 2 {
				t.Fatalf("card %+v", a)
			}
			return
		}
		if time.Now().After(end) {
			t.Fatalf("card %+v (%v)", a, ok)
		}
	}
}

// TestAwayPhoneBeat: a phone counts as watching only while it streams the
// chat and beat within phoneViewTTL; one that went quiet (screen off, app
// frozen) or said it left stops counting, last seen at its last beat.
func TestAwayPhoneBeat(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	old := phoneViewTTL
	phoneViewTTL = 100 * time.Millisecond
	t.Cleanup(func() { phoneViewTTL = old })
	on := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.away.on["a"]
	}
	seen := func() time.Time {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.away.seen["a"]
	}

	d.awayPhone("a", 1)
	if on() {
		t.Fatal("a stream with no beat counts as watching")
	}
	d.awayView("a", true)
	if !on() {
		t.Fatal("a beating phone does not count")
	}
	beat := time.Now()
	time.Sleep(250 * time.Millisecond)
	if on() {
		t.Fatal("a phone that went quiet still counts")
	}
	if s := seen(); s.Before(beat.Add(-50*time.Millisecond)) || s.After(beat.Add(50*time.Millisecond)) {
		t.Errorf("last seen %v, want its last beat %v", s, beat)
	}

	d.awayView("a", true)
	d.awayView("a", false) // the app left the foreground
	if on() {
		t.Fatal("a phone that left still counts")
	}
	d.awayView("a", true)
	d.awayPhone("a", -1) // the stream closed
	if on() {
		t.Fatal("a phone with no stream still counts")
	}
	d.awayPhone("a", 1)
	if on() {
		t.Fatal("a closed stream's beat came back with the next stream")
	}
	d.awayPhone("a", -1)
}

// TestAwayChatTerminal: a chat tab showing its agent's TUI in pane a
// counts as shown while a window focuses that pane, builds its card from
// the pane's transcript under the chat's id, and typing in the pane drops
// the chat's card.
func TestAwayChatTerminal(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	left := time.Now().Add(-10 * time.Minute)
	suggestPane(t, d, "a", awaySnap(left))
	d.o.AwaySummary = func() bool { return false }
	d.mu.Lock()
	d.st.Chats = append(d.st.Chats, model.Chat{ID: "ch", Provider: model.ProviderClaude, Terminal: "a"})
	d.chats["ch"] = &chat{id: "ch"}
	c := &client{}
	d.clients[c] = struct{}{}
	d.mu.Unlock()
	t.Cleanup(func() {
		d.mu.Lock()
		delete(d.clients, c)
		delete(d.chats, "ch")
		d.mu.Unlock()
	})
	focus := func(pane string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		was := c.focus
		c.focus = pane
		d.awayLooks(was)
		d.awayLooks(pane)
	}
	focus("a")
	focus("")
	d.mu.Lock()
	if d.away.on["ch"] {
		t.Error("the chat still shows with no window on its pane")
	}
	d.away.seen["ch"] = left
	d.mu.Unlock()
	focus("a")
	var card model.Away
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		a, ok := d.st.Away["ch"]
		d.mu.Unlock()
		if ok {
			card = a
			break
		}
	}
	if card.Chat != "ch" || card.Commands != 2 || card.Item != "e1" {
		t.Fatalf("the chat's card from its TUI pane: %+v", card)
	}
	d.noteUserKey("a", []byte("x"))
	d.mu.Lock()
	_, ok := d.st.Away["ch"]
	d.mu.Unlock()
	if ok {
		t.Fatal("typing in the TUI's pane kept the chat's card")
	}
}
