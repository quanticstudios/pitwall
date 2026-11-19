package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// paneRead reads pane chat id after seq after and applies it to snap.
func paneRead(t *testing.T, d *Daemon, id string, snap session.Snapshot, after int64) (session.Snapshot, int64) {
	t.Helper()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: id, After: after})
	out, ok := r.(proto.ChatDeltas)
	if !ok {
		t.Fatalf("read %s: %+v", id, r)
	}
	if out.Snapshot != nil {
		snap = session.Snapshot{}
		if err := json.Unmarshal(out.Snapshot, &snap); err != nil {
			t.Fatal(err)
		}
	}
	seq := out.Seq
	for _, b := range out.Deltas {
		var dl session.Delta
		if err := json.Unmarshal(b, &dl); err != nil {
			t.Fatal(err)
		}
		n, err := session.Apply(&snap, seq, dl)
		if err != nil {
			t.Fatal(err)
		}
		snap, seq = n, dl.Seq
	}
	return snap, seq
}

func TestPaneChat(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"list files"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pane b runs Codex: no pane chat, so the read falls through to the chats.
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "b"}); r.(proto.ChatResult).Code != "not-found" {
		t.Fatalf("codex pane read: %+v", r)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.st.Panes[0].AgentMode = "bypassPermissions" // cdang's: the prompt on screen hides the footer
	at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	d.mu.Unlock()

	// Attaching mid-session: the history so far, and the permission prompt
	// on screen as a card for the Bash call it asks about.
	snap, seq := paneRead(t, d, "a", session.Snapshot{}, 0)
	if len(snap.Content) < 2 || snap.Content[0].Kind != session.KindUserPrompt || snap.Content[0].Text != "list files" {
		t.Fatalf("content %+v: want the prompt first", snap.Content)
	}
	if len(snap.PendingApprovals) != 1 || snap.PendingApprovals[0].ID != strconv.FormatInt(at, 10) ||
		snap.PendingApprovals[0].Request == nil || snap.PendingApprovals[0].Request.ToolName != "Bash" {
		t.Fatalf("approvals %+v: want the Bash call's, under the activity's time", snap.PendingApprovals)
	}

	if snap.PermissionMode != "bypassPermissions" {
		t.Errorf("mode %q: want the hooks' bypassPermissions until the footer shows", snap.PermissionMode)
	}

	// Allow presses 1, Deny Esc, on the pane's prompt.
	before := len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatApprove{Chat: "a", Request: snap.PendingApprovals[0].ID, Decision: "allow"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("allow: %+v", r)
	}
	if got := gotAfter(t, f.pane(0), before, 1); got != "1" {
		t.Errorf("allow typed %q, want 1", got)
	}
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatApprove{Chat: "a", Request: snap.PendingApprovals[0].ID, Decision: "deny"})
	if got := gotAfter(t, f.pane(0), before, 1); got != "\x1b" {
		t.Errorf("deny typed %q, want Esc", got)
	}

	// The tool finishes and the turn ends: deltas after seq bring it in.
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	more := `{"type":"user","uuid":"r1","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go"}]}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-11-18T10:00:03Z"}
`
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(more)
	fh.Close()
	snap, seq = paneRead(t, d, "a", snap, seq)
	if len(snap.PendingApprovals) != 0 || len(snap.Turns) != 1 || snap.Turns[0].State != session.TurnCompleted {
		t.Fatalf("after the turn: approvals %+v, turns %+v", snap.PendingApprovals, snap.Turns)
	}
	if snap2, seq2 := paneRead(t, d, "a", snap, seq); seq2 != seq || len(snap2.Content) != len(snap.Content) {
		t.Errorf("a read with nothing new moved seq %d to %d", seq, seq2)
	}

	// Esc stops a running turn; idle, it presses nothing.
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatInterrupt{Chat: "a"})
	if got := gotAfter(t, f.pane(0), before, 1); got != "\x1b" {
		t.Errorf("interrupt typed %q, want Esc", got)
	}
	// Sent mid-turn, a message waits in the chat's queue until the next
	// prompt line takes it.
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "then this"}); r.(proto.ChatResult).Message != "queued" {
		t.Fatalf("busy send: %+v", r)
	}
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.QueuedInputs) != 1 || snap.QueuedInputs[0].Text != "then this" {
		t.Fatalf("queue %+v: want the busy send", snap.QueuedInputs)
	}
	fh, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"user","uuid":"p2","timestamp":"2026-11-18T10:01:00Z","message":{"role":"user","content":"then this"}}` + "\n")
	fh.Close()
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.QueuedInputs) != 0 || len(snap.Turns) != 2 {
		t.Fatalf("after its prompt: queue %+v, %d turns", snap.QueuedInputs, len(snap.Turns))
	}

	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateCompleted
	d.mu.Unlock()
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatInterrupt{Chat: "a"})
	if got := f.pane(0).got()[before:]; got != "" {
		t.Errorf("idle interrupt typed %q", got)
	}

	// With no footer to read, a mode pick is one Shift+Tab.
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatSet{Chat: "a", Mode: "default"})
	if got := gotAfter(t, f.pane(0), before, 3); got != "\x1b[Z" {
		t.Errorf("mode press typed %q, want Shift+Tab", got)
	}
	// With one, it presses Shift+Tab until the footer reads the mode
	// picked, in the TUI's own cycle.
	cycle := []string{"", "  ⏵⏵ accept edits on (shift+tab to cycle)", "  ⏸ plan mode on (shift+tab to cycle)", "  ⏵⏵ bypass permissions on (shift+tab to cycle)"}
	fp := f.pane(0)
	fp.mu.Lock()
	pressed := strings.Count(string(fp.input), "\x1b[Z")
	footer := func() {
		at := (strings.Count(string(fp.input), "\x1b[Z") - pressed) % len(cycle)
		fp.screen = []string{"────────", "❯ ", "────────", cycle[at]}
	}
	footer()
	fp.wrote = footer
	fp.mu.Unlock()
	before = len(fp.got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatSet{Chat: "a", Mode: "plan"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("mode pick: %+v", r)
	}
	if got := fp.got()[before:]; strings.Count(got, "\x1b[Z") != 2 {
		t.Errorf("plan from default typed %q, want Shift+Tab twice", got)
	}
	if r, _ := d.chatRequest(context.Background(), proto.ChatSet{Chat: "a", Mode: "auto"}); r.(proto.ChatResult).Code != "unsupported" {
		t.Errorf("a mode outside the cycle: %+v, want unsupported", r)
	}
	fp.mu.Lock()
	fp.wrote, fp.screen = nil, nil
	fp.mu.Unlock()

	// An image goes in as its pasted path, which Claude Code numbers
	// itself: no [Image #N] token and no evidence line ride along.
	before = len(fp.got())
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...))
	send := proto.ChatSend{Chat: "a", Text: "describe [Image #1] in five words", Attachments: []session.Attachment{
		{ID: "image-1", Kind: "image", MimeType: "image/png", Name: "image-1.png", SizeBytes: 72, Data: png}}}
	if r, _ := d.chatRequest(context.Background(), send); r.(proto.ChatResult).Code != "" {
		t.Fatalf("image send: %+v", r)
	}
	typed := gotAfter(t, fp, before, len("describe in five words"))
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(typed, ".png") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		typed = fp.got()[before:]
	}
	if !strings.Contains(typed, "describe in five words") || !strings.Contains(typed, ".png") || strings.Contains(typed, "[Image #") || strings.Contains(typed, "evidence") {
		t.Errorf("image send typed %q: want the text without its token, and the image's path", typed)
	}

	// A slash command Claude Code's menu offers matches for gets Esc before
	// Enter, which closes the menu, so Enter sends it as typed, not the
	// menu's first match. With no menu up, no Esc, which would stop a turn.
	for _, c := range []struct {
		text string
		menu bool
	}{{"/pwpro", true}, {"/pwprobe twice", false}} {
		fp.mu.Lock()
		fp.screen = []string{"────────", "❯ " + c.text, "────────"}
		if c.menu {
			fp.screen = append([]string{"  /pwprobe-two  Second probe skill. (user)"}, fp.screen...)
		}
		fp.modes.BracketedPaste = true
		fp.mu.Unlock()
		before = len(fp.got())
		if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: c.text}); r.(proto.ChatResult).Code != "" {
			t.Fatalf("send %q: %+v", c.text, r)
		}
		typed := gotAfter(t, fp, before, len(c.text))
		for deadline := time.Now().Add(2 * time.Second); !strings.HasSuffix(typed, "\r") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			typed = fp.got()[before:]
		}
		esc := strings.HasSuffix(strings.TrimSuffix(typed, "\r"), "\x1b")
		if !strings.Contains(typed, c.text) || !strings.HasSuffix(typed, "\r") || esc != c.menu {
			t.Errorf("send %q typed %q: want the text, Esc %v, then Enter", c.text, typed, c.menu)
		}
	}
	fp.mu.Lock()
	fp.screen, fp.modes.BracketedPaste = nil, false
	fp.mu.Unlock()

	// Claude exits: its hooks' process leaves, the provider goes, and so
	// does the pane chat.
	d.mu.Lock()
	d.setProvider("a", "")
	d.mu.Unlock()
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"}); r.(proto.ChatResult).Code != "not-found" {
		t.Fatalf("read after exit: %+v", r)
	}
	d.mu.Lock()
	_, kept := d.paneChats["a"]
	d.mu.Unlock()
	if kept {
		t.Error("the pane chat outlived its claude")
	}
}

// A pane chat's effort is the one the TUI shows, else the effortLevel in
// Claude Code's user settings; with neither it stays unknown. The TUI's
// model goes in as it changes, and a transcript's model stands until then.
func TestPaneChatSettings(t *testing.T) {
	conf := t.TempDir()
	if err := os.WriteFile(filepath.Join(conf, "settings.json"), []byte(`{"effortLevel":"medium","model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := &paneChat{id: "a", snap: session.Snapshot{SessionID: "a"}}
	pc.settings(paneLive{conf: conf})
	if pc.snap.Effort == nil || *pc.snap.Effort != "medium" || pc.snap.Model != nil {
		t.Fatalf("from the settings: effort %v, model %v; want medium and none", pc.snap.Effort, pc.snap.Model)
	}
	tui := paneLive{conf: conf, tui: phoneSettings{model: "Opus 5.5", effort: "xhigh"}}
	pc.settings(tui)
	if *pc.snap.Effort != "xhigh" || pc.snap.Model == nil || *pc.snap.Model != "Opus 5.5" {
		t.Fatalf("from the TUI: effort %v, model %v", *pc.snap.Effort, pc.snap.Model)
	}
	pc.reduce([]session.Event{{Type: session.EventSettings, Model: "claude-sonnet-5-5"}})
	pc.settings(tui)
	if *pc.snap.Model != "claude-sonnet-5-5" {
		t.Errorf("model %q: the TUI's unchanged one took over the transcript's", *pc.snap.Model)
	}
	none := &paneChat{id: "b", snap: session.Snapshot{SessionID: "b"}}
	none.settings(paneLive{conf: t.TempDir()})
	if none.snap.Effort != nil {
		t.Errorf("effort %q with no source: want unknown", *none.snap.Effort)
	}
}
