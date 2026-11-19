package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// turnLines is turn i of a transcript: its prompt, a Bash call whose
// output is out, and Claude Code's mark at the turn's end.
func turnLines(i int, out string) string {
	o, _ := json.Marshal(out)
	return fmt.Sprintf(`{"type":"user","uuid":"p%[1]d","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"prompt %[1]d"}}
{"type":"assistant","uuid":"a%[1]d","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m%[1]d","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t%[1]d","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","uuid":"r%[1]d","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%[1]d","content":%[2]s}]}}
{"type":"system","subtype":"turn_duration","uuid":"d%[1]d","timestamp":"2026-11-18T10:00:03Z"}
`, i, o)
}

// TestTailStart: the tail starts at the prompt of the turn holding the
// tailItems-th chat line from the end, after the last compaction, at the
// first whole line when no prompt is in reach, and takes in a line longer
// than the reach whole.
func TestTailStart(t *testing.T) {
	start := func(s string) int64 {
		t.Helper()
		n, err := tailStart(strings.NewReader(s), int64(len(s)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	var b strings.Builder
	for i := range 300 {
		b.WriteString(turnLines(i, "ok"))
	}
	s := b.String()
	if got, want := start(s), int64(strings.Index(s, `{"type":"user","uuid":"p200"`)); got != want { // two chat lines a turn
		t.Errorf("start %d, want turn 200's prompt at %d", got, want)
	}
	if got := start(turnLines(0, "ok")); got != 0 {
		t.Errorf("a short transcript starts at %d, want 0", got)
	}

	compacted := s[:strings.Index(s, `{"type":"user","uuid":"p290"`)] +
		`{"type":"system","subtype":"compact_boundary","uuid":"c"}` + "\n" +
		`{"type":"user","isCompactSummary":true,"uuid":"cs","message":{"role":"user","content":"This session is being continued"}}` + "\n" +
		s[strings.Index(s, `{"type":"user","uuid":"p290"`):]
	if got, want := start(compacted), int64(strings.Index(compacted, `{"type":"user","uuid":"p290"`)); got != want {
		t.Errorf("compacted: start %d, want the prompt after the compaction at %d", got, want)
	}
	if got := start(compacted[:strings.Index(compacted, `{"type":"user","uuid":"p290"`)]); got == 0 || got > int64(strings.Index(compacted, `"uuid":"c"`)) {
		t.Errorf("before the compaction: start %d, want a page that goes past it", got)
	}

	big := turnLines(0, strings.Repeat("x", tailBytes+1000)) // one line longer than the reach
	if got := start(big); got != 0 {
		t.Errorf("long line: start %d, want 0", got)
	}

	var calls strings.Builder // a single turn's calls, past the reach: no prompt in it
	calls.WriteString(turnLines(0, "ok"))
	for calls.Len() < tailBytes+(1<<20) {
		calls.WriteString(strings.SplitAfter(turnLines(1, strings.Repeat("y", 20000)), "\n")[2])
	}
	c := calls.String()
	if got := start(c); got <= 0 || c[got-1] != '\n' {
		t.Errorf("no prompt in reach: start %d, want a whole line after the reach's first", got)
	}
}

// TestPaneChatPages: a long transcript reads as its tail with a cursor,
// pages back to its start whole and in order, each page's turns ended;
// an item cut for the wire comes whole when asked for; a big output
// arrives as a snapshot, not deltas; and a transcript that grew past the
// reach unread reads its tail again.
func TestPaneChatPages(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var b strings.Builder
	for i := range 400 {
		out := "ok"
		if i%10 == 0 {
			out = strings.Repeat("line\n", 20000) // 100 KB
		}
		b.WriteString(turnLines(i, out))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()

	read := func(m proto.ChatRead) proto.ChatDeltas {
		t.Helper()
		m.Chat = "a"
		r, _ := d.chatRequest(context.Background(), m)
		out, ok := r.(proto.ChatDeltas)
		if !ok {
			t.Fatalf("read %+v: %+v", m, r)
		}
		return out
	}
	first := read(proto.ChatRead{})
	var tail session.Snapshot
	json.Unmarshal(first.Snapshot, &tail)
	if first.Older == 0 || len(tail.Content) == 0 || len(tail.Content) > 2*tailItems || tail.Content[0].ID != "p300" {
		t.Fatalf("tail: older %d, %d items from %v", first.Older, len(tail.Content), tail.Content[0].ID)
	}
	all := tail.Content
	for before := first.Older; before > 0; {
		p := read(proto.ChatRead{Before: before})
		var s session.Snapshot
		json.Unmarshal(p.Snapshot, &s)
		if p.Page != before || p.Older >= before || len(s.Content) == 0 || s.Busy() {
			t.Fatalf("page before %d: older %d, %d items, busy %v", before, p.Older, len(s.Content), s.Busy())
		}
		all, before = append(s.Content, all...), p.Older
	}
	if len(all) != 800 || all[0].ID != "p0" || all[799].ID != "t399" {
		t.Fatalf("paged %d items, %s to %s: want 800, p0 to t399", len(all), all[0].ID, all[len(all)-1].ID)
	}
	for _, it := range all {
		if it.ID == "t10" && (!it.Cut || len(it.Tool.Output) > session.ClipBytes+100) {
			t.Fatalf("t10 on the wire: cut %v, %d bytes", it.Cut, len(it.Tool.Output))
		}
	}
	for _, id := range []string{"t10", "t390"} { // from a page, from the tail
		var it session.Item
		json.Unmarshal(read(proto.ChatRead{Item: id}).Item, &it)
		if it.Cut || len(it.Tool.Output) != 100000 {
			t.Fatalf("%s whole: cut %v, %d bytes", id, it.Cut, len(it.Tool.Output))
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(turnLines(400, strings.Repeat("z", 2<<20)))
	got := read(proto.ChatRead{After: first.Seq})
	if got.Snapshot == nil || got.Deltas != nil || got.Older != first.Older {
		t.Fatalf("a 2 MB output came as %d deltas, snapshot %v, older %d", len(got.Deltas), got.Snapshot != nil, got.Older)
	}
	for i := 401; i < 460; i++ {
		f.WriteString(turnLines(i, strings.Repeat("w", 100000)))
	}
	f.Close()
	again := read(proto.ChatRead{After: got.Seq})
	if again.Snapshot == nil || again.Older <= first.Older || len(again.Snapshot) > snapBytes {
		t.Fatalf("after %d MB unread: snapshot %v, older %d, %d bytes", 6, again.Snapshot != nil, again.Older, len(again.Snapshot))
	}
}

// TestChatReadWindow: a stream chat's snapshot holds its newest items up
// to snapBytes once cut, with a cursor to pages of the rest back to its
// first item; an item comes whole when asked for.
func TestChatReadWindow(t *testing.T) {
	c := &chat{id: "c", snap: session.Snapshot{SessionID: "c"}}
	for i := range 3000 {
		c.snap.Content = append(c.snap.Content, session.Item{ID: fmt.Sprint("i", i), Kind: session.KindToolCall,
			Tool: &session.ToolCall{ToolName: "Bash", Status: "completed", Output: strings.Repeat("o", 40000)}})
	}
	read := func(r proto.ChatRead) (proto.ChatDeltas, session.Snapshot) {
		t.Helper()
		out, err := c.read(r)
		if err != nil {
			t.Fatal(err)
		}
		var s session.Snapshot
		json.Unmarshal(out.Snapshot, &s)
		return out, s
	}
	out, s := read(proto.ChatRead{})
	if out.Older == 0 || len(out.Snapshot) > snapBytes+(64<<10) || s.Content[len(s.Content)-1].ID != "i2999" || !s.Content[0].Cut {
		t.Fatalf("snapshot: older %d, %d bytes, %d items", out.Older, len(out.Snapshot), len(s.Content))
	}
	n := len(s.Content)
	for before := out.Older; before > 0; {
		p, s := read(proto.ChatRead{Before: before})
		if p.Page != before || p.Older >= before || s.Content[len(s.Content)-1].ID != fmt.Sprint("i", before-1) {
			t.Fatalf("page before %d: older %d, %d items", before, p.Older, len(s.Content))
		}
		n, before = n+len(s.Content), p.Older
	}
	if n != 3000 {
		t.Fatalf("paged %d items, want 3000", n)
	}
	it, _ := read(proto.ChatRead{Item: "i7"})
	var whole session.Item
	if json.Unmarshal(it.Item, &whole); whole.Cut || len(whole.Tool.Output) != 40000 {
		t.Fatalf("i7 whole: cut %v, %d bytes", whole.Cut, len(whole.Tool.Output))
	}
	if _, err := c.read(proto.ChatRead{Item: "gone"}); chatCode(err) != "not-found" {
		t.Fatalf("unknown item: %v", err)
	}
}

// TestPaneChatUnreadable: a transcript not written yet reads as an empty
// chat; one the daemon cannot read answers the first read with why.
func TestPaneChatUnreadable(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"}); r.(proto.ChatDeltas).Snapshot == nil {
		t.Fatalf("no transcript yet: %+v", r)
	}
	os.WriteFile(path, []byte(turnLines(0, "ok")), 0o000)
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this system reads a mode 000 file (Windows, root)")
	}
	d.mu.Lock()
	delete(d.paneChats, "a") // a window that reads it first now
	d.mu.Unlock()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"})
	if res, ok := r.(proto.ChatResult); !ok || res.Code == "" || !strings.Contains(res.Message, "permission denied") {
		t.Fatalf("unreadable transcript: %+v", r)
	}
}

// TestPaneChatImage: a prompt's pasted image comes without its data in
// the snapshot, and whole when the window asks for the item.
func TestPaneChatImage(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	data := strings.Repeat("iVBORw0K", 4096)
	line := `{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":[` +
		`{"type":"text","text":"what is this [Image #1]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"})
	var s session.Snapshot
	json.Unmarshal(r.(proto.ChatDeltas).Snapshot, &s)
	if len(s.Content) == 0 || len(s.Content[0].Attachments) != 1 || s.Content[0].Attachments[0].Data != "" || !s.Content[0].Cut {
		t.Fatalf("snapshot prompt: %+v", s.Content)
	}
	r, _ = d.chatRequest(context.Background(), proto.ChatRead{Chat: "a", Item: s.Content[0].ID})
	var it session.Item
	json.Unmarshal(r.(proto.ChatDeltas).Item, &it)
	if len(it.Attachments) != 1 || it.Attachments[0].Data != data || it.Cut {
		t.Fatalf("whole prompt: %d attachments, cut %v", len(it.Attachments), it.Cut)
	}
}
