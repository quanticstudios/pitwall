package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
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
	// Sent mid-turn, a message waits in the chat's queue, untyped, until
	// the turn ends; its row can be moved, edited, removed or sent now.
	queue := func(m proto.ChatQueue) {
		t.Helper()
		m.Chat = "a"
		if r, _ := d.chatRequest(context.Background(), m); r.(proto.ChatResult).Code != "" {
			t.Fatalf("%s: %+v", m.Action, r)
		}
	}
	texts := func(s session.Snapshot) string {
		var out []string
		for _, q := range s.QueuedInputs {
			out = append(out, q.Text)
		}
		return strings.Join(out, "|")
	}
	before = len(f.pane(0).got())
	for _, text := range []string{"then this", "and that", "drop me"} {
		if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: text}); r.(proto.ChatResult).Message != "queued" {
			t.Fatalf("busy send: %+v", r)
		}
	}
	snap, seq = paneRead(t, d, "a", snap, seq)
	if got := texts(snap); got != "then this|and that|drop me" {
		t.Fatalf("queue %q: want the busy sends", got)
	}
	ids := map[string]string{}
	for _, q := range snap.QueuedInputs {
		ids[q.Text] = q.ID
	}
	queue(proto.ChatQueue{Action: "move", ID: ids["and that"], Index: 0})
	queue(proto.ChatQueue{Action: "edit", ID: ids["then this"], Text: "then this, edited"})
	queue(proto.ChatQueue{Action: "remove", ID: ids["drop me"]})
	if snap, seq = paneRead(t, d, "a", snap, seq); texts(snap) != "and that|then this, edited" {
		t.Fatalf("queue %q after move, edit and remove", texts(snap))
	}
	if got := f.pane(0).got()[before:]; got != "" {
		t.Fatalf("typed %q mid-turn: the queue holds it", got)
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"> "} // the prompt answered: Claude works at its prompt
	f.pane(0).mu.Unlock()
	queue(proto.ChatQueue{Action: "now", ID: ids["and that"]})
	if got := gotAfter(t, f.pane(0), before, len("and that")); !strings.Contains(got, "and that") {
		t.Fatalf("send now typed %q", got)
	}
	if snap, seq = paneRead(t, d, "a", snap, seq); texts(snap) != "then this, edited" {
		t.Fatalf("queue %q after send now", texts(snap))
	}
	// Stop holds the queue until the next send or Resume.
	d.chatRequest(context.Background(), proto.ChatInterrupt{Chat: "a"})
	if snap, seq = paneRead(t, d, "a", snap, seq); !snap.QueuePaused {
		t.Fatal("an interrupt did not hold the queue")
	}
	queue(proto.ChatQueue{Action: "resume"})
	if snap, seq = paneRead(t, d, "a", snap, seq); snap.QueuePaused {
		t.Fatal("resume left the queue held")
	}
	// The turn ends: deliver types the row and it goes.
	oldQuiet, oldSettle := sendQuiet.set(time.Millisecond), sendSettle.set(time.Millisecond)
	defer func() { sendQuiet.set(oldQuiet); sendSettle.set(oldSettle) }()
	before = len(f.pane(0).got())
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateCompleted
	d.live.hookAt["a"] = time.Now()
	d.wake("a", 0)
	d.mu.Unlock()
	if got := gotAfter(t, f.pane(0), before, len("then this, edited")); !strings.Contains(got, "then this, edited") {
		t.Fatalf("turn end typed %q", got)
	}
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.QueuedInputs) != 0 {
		t.Fatalf("queue %q after delivery", texts(snap))
	}
	// A turn that ended with a background shell running takes a send at
	// once: Claude Code sits at its prompt.
	d.mu.Lock()
	ai := d.activityIndex("a")
	d.st.Activities[ai].State, d.st.Activities[ai].Background = model.StateWorking, true
	d.mu.Unlock()
	before = len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "while it sleeps"}); r.(proto.ChatResult).Message == "queued" {
		t.Fatalf("a send after the turn queued: %+v", r)
	}
	if got := gotAfter(t, f.pane(0), before, len("while it sleeps")); !strings.Contains(got, "while it sleeps") {
		t.Fatalf("background send typed %q", got)
	}
	d.mu.Lock()
	d.st.Activities[ai].Background = false
	d.mu.Unlock()

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
	// Several lines go in as a paste each, joined by Ctrl+J, a Tab as a
	// space; a pasted-text attachment is one paste after a blank line, with
	// no evidence line.
	fp.mu.Lock()
	fp.screen = []string{"────────", "❯ ", "────────"}
	fp.mu.Unlock()
	before = len(fp.got())
	send = proto.ChatSend{Chat: "a", Text: "fix\tthis\nplease", Attachments: []session.Attachment{
		{ID: "t", Kind: "pasted-text", Name: "log.txt", Text: "l1\nl2\nl3\nl4"}}}
	if r, _ := d.chatRequest(context.Background(), send); r.(proto.ChatResult).Code != "" {
		t.Fatalf("multi-line send: %+v", r)
	}
	want := "\x1b[200~fix this\x1b[201~\n\x1b[200~please\x1b[201~\n\n\x1b[200~l1\nl2\nl3\nl4\x1b[201~\r"
	if got := gotAfter(t, fp, before, len(want)); got != want {
		t.Errorf("multi-line send typed %q, want %q", got, want)
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

// The model the TUI showed stays on the chip once the transcript appears:
// reading it from the top builds a new snapshot, before any line names a
// model.
func TestPaneChatModelSurvivesTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	live := paneLive{p: &fakePane{}, tui: phoneSettings{model: "Opus 5.5"}}
	pc.catchUp(live)
	if pc.snap.Model == nil || *pc.snap.Model != "Opus 5.5" {
		t.Fatalf("before the transcript: model %v", pc.snap.Model)
	}
	prompt := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	pc.catchUp(live)
	if pc.snap.Model == nil || *pc.snap.Model != "Opus 5.5" {
		t.Errorf("after the first prompt: model %v, want the TUI's Opus 5.5", pc.snap.Model)
	}
}

// A pane chat's context meter reads the last main-thread assistant line's
// usage, input plus cache creation and reads, out of the model's window:
// 1M when the TUI names the model's 1M version.
func TestPaneChatContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T10:00:01Z","message":{"id":"m1","model":"claude-opus-4-6","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":4000,"cache_read_input_tokens":36000,"output_tokens":90}}}
{"type":"assistant","uuid":"s1","isSidechain":true,"timestamp":"2026-11-19T10:00:02Z","message":{"id":"m2","model":"claude-opus-4-6","content":[],"usage":{"input_tokens":150000}}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(paneLive{p: &fakePane{}})
	if u := pc.snap.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 40002 || u.MaxTokens == nil || *u.MaxTokens != 200_000 {
		t.Fatalf("usage %+v: want 40002 of 200K, the sidechain's skipped", u)
	}
	pc.catchUp(paneLive{p: &fakePane{}, tui: phoneSettings{model: "Opus 4.6 1M"}})
	if u := pc.snap.Usage; *u.MaxTokens != 1_000_000 {
		t.Errorf("window %d with the TUI on Opus 4.6 1M: want 1M", *u.MaxTokens)
	}
	// What Claude Code 2.1.330 reports as modelUsage.contextWindow.
	for _, c := range []struct {
		model, tui string
		used, want int64
	}{
		{"claude-opus-5-5", "", 300_000, 1_000_000},
		{"claude-opus-5-5", "Opus 5.5", 40_000, 1_000_000},
		{"claude-sonnet-5-5", "", 0, 1_000_000},
		{"claude-haiku-5-5", "", 0, 1_000_000},
		{"claude-fable-5-2", "", 0, 1_000_000},
		{"claude-opus-5", "", 0, 1_000_000},
		{"", "", 0, 1_000_000},
		{"claude-opus-5-5[1m]", "", 0, 1_000_000},
		{"claude-opus-4-6", "", 0, 200_000},
		{"claude-3-5-sonnet-20241022", "", 0, 200_000},
		{"", "Sonnet 4.5", 0, 200_000},
		{"claude-opus-4-6", "", 250_000, 1_000_000}, // past 200K it can only be the larger window
	} {
		if w := contextWindow(c.model, c.tui, c.used); w != c.want {
			t.Errorf("contextWindow(%q, %q, %d) = %d, want %d", c.model, c.tui, c.used, w, c.want)
		}
	}
}

// TestPaneChatSubagents: a subagent's calls come from its own transcript,
// which its meta file ties to the Agent call, even once it has ended.
func TestPaneChatSubagents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	for _, l := range []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-19T20:40:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"count lines"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T20:40:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"ag","name":"Agent","input":{"description":"Count lines","subagent_type":"general-purpose"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-11-19T20:40:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"ag","content":[{"type":"text","text":"Async agent launched successfully."}]}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"x1"}}`,
		// It ended before the chat read its transcript, as on a replay.
		`{"type":"user","uuid":"n1","timestamp":"2026-11-19T20:40:09Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>x1</task-id>\n<tool-use-id>ag</tool-use-id>\n<status>completed</status>\n<summary>Agent finished</summary>\n<result>2 notes.txt</result>\n</task-notification>"}}`,
	} {
		pc.reduce(pc.tr.Line([]byte(l)))
	}
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "agent-x1.meta.json"), []byte(`{"agentType":"general-purpose","toolUseId":"ag"}`), 0o600)
	os.WriteFile(filepath.Join(sub, "agent-x1.jsonl"), []byte(
		`{"type":"user","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:02Z","message":{"role":"user","content":"Run wc -l notes.txt"}}
{"type":"assistant","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:03Z","message":{"id":"s1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"wc -l notes.txt"}}]}}
{"type":"user","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"b1","content":"2 notes.txt"}]}}
`), 0o600)
	pc.subagents()
	var calls []session.ToolCall
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" {
			calls = it.Tool.SubagentCalls
		}
	}
	if len(calls) != 1 || calls[0].ToolName != "Bash" || calls[0].Status != "completed" {
		t.Fatalf("subagent calls %+v: want its Bash call, done", calls)
	}
	pc.subagents() // read again: nothing new, nothing twice
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" && len(it.Tool.SubagentCalls) != 1 {
			t.Fatalf("a second read added calls: %+v", it.Tool.SubagentCalls)
		}
	}
}

// A background task's end marks the agent done just as Claude Code starts
// the turn its notification wakes: that turn runs on until Claude Code
// ends it, and its reply stays its own, not the previous turn's.
func TestPaneChatNotifiedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-20T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"say banana"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"banana"}],"usage":{"input_tokens":2,"output_tokens":3}}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-11-20T10:00:02Z"}
{"type":"user","uuid":"n1","timestamp":"2026-11-20T10:00:20Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>t9</tool-use-id>\n<output-file>/tmp/b1.output</output-file>\n<status>completed</status>\n<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 11, 20, 10, 0, 20, 500_000_000, time.UTC)
	live := paneLive{p: &fakePane{}, act: model.Activity{State: model.StateCompleted, UpdatedAt: ended}}
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(live)
	if n := len(pc.snap.Turns); n != 2 || pc.snap.Turns[1].State != session.TurnRunning {
		t.Fatalf("turns %+v: want the notified one running", pc.snap.Turns)
	}
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"assistant","uuid":"a2","timestamp":"2026-11-20T10:00:21Z","message":{"id":"m2","model":"claude-opus-5-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":2,"output_tokens":1}}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-11-20T10:00:22Z"}
`)
	fh.Close()
	pc.catchUp(live)
	last := pc.snap.Content[len(pc.snap.Content)-1]
	if last.Text != "done" || last.TurnID == nil || *last.TurnID != pc.snap.Turns[1].ID || pc.snap.Turns[1].State != session.TurnCompleted {
		t.Fatalf("last item %+v in turns %+v: want done in the notified turn, completed", last, pc.snap.Turns)
	}
}

// A background shell that ends while the next prompt's turn runs updates
// its call from the earlier turn. That is no reply in the running turn, so
// a Stop hook read before Claude Code wrote the turn's answer leaves it
// running, and the answer stays its turn's own.
func TestPaneChatBackgroundNoticeIsNoReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-20T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"sleep in the background"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T10:00:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"t9","name":"Bash","input":{"command":"sleep 40","run_in_background":true}}],"usage":{"input_tokens":2,"output_tokens":3}}}
{"type":"user","uuid":"r1","timestamp":"2026-11-20T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t9","content":"Command running in background with ID: b1."}]},"toolUseResult":{"stdout":"","stderr":"","backgroundTaskId":"b1"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-11-20T10:00:03Z","message":{"id":"m2","model":"claude-sonnet-5-5","content":[{"type":"text","text":"started"}],"usage":{"input_tokens":2,"output_tokens":3}}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-11-20T10:00:04Z"}
{"type":"user","uuid":"p2","timestamp":"2026-11-20T10:00:40Z","origin":{"kind":"human"},"message":{"role":"user","content":"say A"}}
{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-20T10:00:40.1Z","content":"<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>t9</tool-use-id>\n<status>completed</status>\n<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	stopped := paneLive{p: &fakePane{}, act: model.Activity{State: model.StateCompleted, UpdatedAt: time.Date(2026, 11, 20, 10, 0, 41, 0, time.UTC)}}
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(stopped)
	if n := len(pc.snap.Turns); n != 2 || pc.snap.Turns[1].State != session.TurnRunning {
		t.Fatalf("turns %+v: want the second running until its answer", pc.snap.Turns)
	}
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"assistant","uuid":"a3","timestamp":"2026-11-20T10:00:41Z","message":{"id":"m3","model":"claude-sonnet-5-5","content":[{"type":"text","text":"A"}],"usage":{"input_tokens":2,"output_tokens":1}}}
`)
	fh.Close()
	pc.catchUp(stopped)
	last := pc.snap.Content[len(pc.snap.Content)-1]
	if last.Text != "A" || last.TurnID == nil || *last.TurnID != pc.snap.Turns[1].ID {
		t.Fatalf("last item %+v: want A in the second turn %+v", last, pc.snap.Turns)
	}
}

// A pane chat's approval says when its prompt offers no Allow for this
// session, as Claude Code's prompt for a hook that asks does not, and
// stops saying so once the screen shows that answer.
func TestPaneChatApprovalOffers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-20T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"touch a"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T10:00:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"touch a"}}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 20, 10, 0, 2, 0, time.UTC)
	act := model.Activity{Provider: model.ProviderClaude, State: model.StatePendingApproval, Detail: "Bash\ntouch a", UpdatedAt: at}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(paneLive{p: &fakePane{}, act: act})
	if n := len(pc.snap.PendingApprovals); n != 1 || !pc.snap.PendingApprovals[0].NoSession {
		t.Fatalf("approvals %+v: want one with no Allow for session, the screen showing none", pc.snap.PendingApprovals)
	}
	act.Buttons = []string{agent.AllowAlways}
	pc.catchUp(paneLive{p: &fakePane{}, act: act})
	if n := len(pc.snap.PendingApprovals); n != 1 || pc.snap.PendingApprovals[0].NoSession {
		t.Errorf("approvals %+v: want Allow for session once the screen offers it", pc.snap.PendingApprovals)
	}
	// A hook that asked prints its reason over the prompt: the card says it.
	act.Buttons = nil
	screen := []string{" Bash command", " touch a", " │ Hook PreToolUse:Bash requires confirmation for this command:",
		" │ Ask before touching files [settings]", " Do you want to proceed?", " ❯ 1. Yes", "   2. No"}
	pc.catchUp(paneLive{p: &fakePane{screen: screen}, act: act})
	if ap := pc.snap.PendingApprovals[0]; ap.Detail == nil || *ap.Detail != "PreToolUse hook: Ask before touching files" || !ap.NoSession {
		t.Errorf("approval %+v: want the hook's reason as its detail, and no Allow for session", ap)
	}
}

// A chat read afresh, as after a restart, drops the turns a rewind left
// behind even when its tail starts after the line the rewind went back
// to, as after a compaction on the abandoned branch with a turn after it:
// the page before the tail would show them, since the prompt that
// branched off is not the first line after it.
func TestPaneChatRewindReadAfresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","parentUuid":null,"timestamp":"2026-11-20T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"touch a"}}
{"type":"assistant","uuid":"a1","parentUuid":"p1","timestamp":"2026-11-20T10:00:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"text","text":"made a"}]}}
{"type":"user","uuid":"p2","parentUuid":"a1","timestamp":"2026-11-20T10:00:10Z","origin":{"kind":"human"},"message":{"role":"user","content":"rm a"}}
{"type":"assistant","uuid":"a2","parentUuid":"p2","timestamp":"2026-11-20T10:00:11Z","message":{"id":"m2","model":"claude-sonnet-5-5","content":[{"type":"text","text":"removed a"}]}}
{"type":"system","subtype":"compact_boundary","uuid":"c1","parentUuid":null,"logicalParentUuid":"a2","timestamp":"2026-11-20T10:00:20Z","content":"Conversation compacted"}
{"type":"user","uuid":"s1","parentUuid":"c1","isCompactSummary":true,"timestamp":"2026-11-20T10:00:21Z","message":{"role":"user","content":"Summary: touched and removed a."}}
{"type":"user","uuid":"p4","parentUuid":"s1","timestamp":"2026-11-20T10:00:30Z","origin":{"kind":"human"},"message":{"role":"user","content":"remember cherry"}}
{"type":"assistant","uuid":"a4","parentUuid":"p4","timestamp":"2026-11-20T10:00:31Z","message":{"id":"m4","model":"claude-sonnet-5-5","content":[{"type":"text","text":"noted cherry"}]}}
{"type":"user","uuid":"p3","parentUuid":"a1","timestamp":"2026-11-20T10:01:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"list commands"}}
{"type":"assistant","uuid":"a3","parentUuid":"p3","timestamp":"2026-11-20T10:01:01Z","message":{"id":"m3","model":"claude-sonnet-5-5","content":[{"type":"text","text":"only touch a"}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(paneLive{p: &fakePane{}})
	var texts []string
	for _, it := range pc.snap.Content {
		if it.Text != "" {
			texts = append(texts, it.Text)
		}
	}
	got := strings.Join(texts, " | ")
	if strings.Contains(got, "rm a") || strings.Contains(got, "removed a") || strings.Contains(got, "cherry") || !strings.Contains(got, "list commands") || !strings.Contains(got, "made a") {
		t.Errorf("items %q: want the rewound branch's turns gone and the live ones kept", got)
	}
}
