package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestChatFindTab: a chat tab's find looks in all its items, case folded,
// the end of one the snapshot cuts too, over the socket. A chat tab's
// snapshot holds every item, the ones before the window's snapshot too
// (TestChatReadWindow), and the find looks in that.
func TestChatFindTab(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderCodex: prov}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderCodex, Dir: t.TempDir(), Prompt: "Find the needle"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	id, drv := r.Chat, prov.driver(t)
	drv.event(session.Event{Type: session.EventContent, ItemID: "old", Kind: session.KindAssistantText, Text: "An old NEEDLE"})
	drv.event(session.Event{Type: session.EventContent, ItemID: "out", Kind: session.KindCommandOutput, Text: "needle in output"})
	long := strings.Repeat("hay ", 10000) + "needle"
	drv.event(session.Event{Type: session.EventContent, ItemID: "long", Kind: session.KindAssistantText, Text: long})
	drv.end(session.TurnCompleted)
	waitChat(gui, "turn done", func(s model.State) bool { return chatOf(s).State != session.StateRunning })

	var snap session.Snapshot
	json.Unmarshal(chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id}).Snapshot, &snap)
	if i := slices.IndexFunc(snap.Content, func(it session.Item) bool { return it.ID == "long" }); i < 0 || !snap.Content[i].Cut {
		t.Fatal("the snapshot does not cut the long item")
	}

	got := chatCall[proto.ChatFindResult](gui, proto.ChatFind{Chat: id, Query: " Needle "})
	var ids []string
	for _, h := range got.Hits {
		ids = append(ids, h.Item)
	}
	if got.Err != "" || got.More || got.Query != " Needle " || len(ids) != 3 || ids[1] != "old" || ids[2] != "long" {
		t.Fatalf("find: %+v", got)
	}
	if h := got.Hits[2]; len(h.At) != 1 || h.At[0] != 40000 || h.Runes != len(long) {
		t.Errorf("the cut item's match: %+v", h)
	}
	if got := chatCall[proto.ChatFindResult](gui, proto.ChatFind{Chat: id, Query: "  "}); got.Err != "" || got.Hits != nil {
		t.Errorf("empty query: %+v", got)
	}
	if got := chatCall[proto.ChatFindResult](gui, proto.ChatFind{Chat: "nope", Query: "x"}); got.Err == "" || got.Chat != "nope" {
		t.Errorf("no such chat: %+v", got)
	}
}

// TestPaneChatFind: a Claude Code pane chat's find reads its transcript
// back to the start, through the pages a window reads, so every hit is an
// item the window reaches by paging; past the limit it keeps the newest.
func TestPaneChatFind(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var b strings.Builder
	for i := range 400 {
		out := "ok"
		if i%10 == 0 {
			out = strings.Repeat("line\n", 20000)
		}
		b.WriteString(turnLines(i, out))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()

	got := d.chatFind(proto.ChatFind{Chat: "a", Query: "PROMPT 7"})
	var ids []string
	for _, h := range got.Hits {
		ids = append(ids, h.Item)
	}
	want := []string{"p7", "p70", "p71", "p72", "p73", "p74", "p75", "p76", "p77", "p78", "p79"}
	if got.Err != "" || got.More || !slices.Equal(ids, want) {
		t.Fatalf("find: %v, err %q, more %v; want %v", ids, got.Err, got.More, want)
	}
	// Each is in the history the window pages back to.
	read := func(m proto.ChatRead) proto.ChatDeltas {
		t.Helper()
		m.Chat = "a"
		r, _ := d.chatRequest(context.Background(), m)
		return r.(proto.ChatDeltas)
	}
	first := read(proto.ChatRead{})
	var s session.Snapshot
	json.Unmarshal(first.Snapshot, &s)
	seen := map[string]bool{}
	for _, it := range s.Content {
		seen[it.ID] = true
	}
	if seen["p7"] {
		t.Fatal("the tail holds p7: the test needs it in a page")
	}
	for before := first.Older; before > 0; {
		p := read(proto.ChatRead{Before: before})
		var s session.Snapshot
		json.Unmarshal(p.Snapshot, &s)
		for _, it := range s.Content {
			seen[it.ID] = true
		}
		before = p.Older
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("hit %s is in no page", id)
		}
	}

	pc, l, _ := d.paneChatOf("a")
	hits, more, err := pc.find(l, session.Fold("prompt 7"), 3)
	if err != nil || !more || len(hits) != 3 || hits[0].Item != "p77" || hits[2].Item != "p79" {
		t.Errorf("limit 3: %+v, more %v, err %v", hits, more, err)
	}
}

// TestPaneChatFindProviders: Codex and Antigravity pane chats find in
// their transcripts too.
func TestPaneChatFindProviders(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	for _, c := range []struct {
		pane, file, query string
		prov              model.Provider
	}{
		{"b", "../session/codex/testdata/rollout.jsonl", "UPDATE_PLAN", model.ProviderCodex},
		{"c", "../session/antigravity/testdata/transcript_full.jsonl", "Notes.TXT", model.ProviderAntigravity},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), filepath.Base(c.file))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == c.pane })
		d.st.Panes[i].Transcript, d.st.Panes[i].Provider = path, c.prov
		d.mu.Unlock()
		got := d.chatFind(proto.ChatFind{Chat: c.pane, Query: c.query})
		if got.Err != "" || len(got.Hits) == 0 {
			t.Errorf("%s: %+v", c.prov, got)
			continue
		}
		var snap session.Snapshot
		r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: c.pane})
		json.Unmarshal(r.(proto.ChatDeltas).Snapshot, &snap)
		i = slices.IndexFunc(snap.Content, func(it session.Item) bool { return it.ID == got.Hits[0].Item })
		if i < 0 || !strings.Contains(strings.ToLower(session.FindText(&snap.Content[i])), strings.ToLower(c.query)) {
			t.Errorf("%s: hit %+v is not an item with the query", c.prov, got.Hits[0])
		}
	}
}
