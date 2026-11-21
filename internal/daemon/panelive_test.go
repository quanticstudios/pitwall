package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// gridPane is a fakePane whose screen is rows drawn by the emulator, so
// wide glyphs such as Claude Code's bullets read as they do live.
type gridPane struct {
	fakePane
	rows []string
}

func (p *gridPane) Snapshot() vt.Grid {
	e := vt.New(80, len(p.rows), nil)
	e.Write([]byte(strings.Join(p.rows, "\r\n")))
	return e.Snapshot()
}

// claudeScreen is Claude Code at 80 columns streaming body under prompt.
func claudeScreen(prompt string, body ...string) []string {
	rule := strings.Repeat("─", 80)
	rows := append([]string{"❯ " + prompt, ""}, body...)
	return append(rows, "", "✻ Moseying… (1s · ↓ 5 tokens)", rule, "❯ ", rule, "  ⏵⏵ accept edits on (shift+tab to cycle)")
}

// The reply Claude Code streams shows in Live while the transcript lacks
// it, and gives way to the transcript's message in the same revision: a
// reader applying the deltas never holds both, nor neither once it shows.
// The prompt the chat sent shows at once and goes when its line lands.
func TestPaneChatLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	prompt := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"explain tcp"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	pane := &gridPane{rows: claudeScreen("explain tcp")}
	l := paneLive{p: pane}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(l)
	// reader is the window: after each read, it applies the read's deltas
	// at once, then draws.
	reader, seq := pc.snap, pc.seq
	read := func(when string) (replies int) {
		t.Helper()
		for _, b := range pc.read(seq).Deltas {
			var d session.Delta
			if err := json.Unmarshal(b, &d); err != nil {
				t.Fatal(err)
			}
			n, err := session.Apply(&reader, seq, d)
			if err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			reader, seq = n, d.Seq
		}
		for _, it := range append(slices.Clone(reader.Content), reader.Live...) {
			if it.Kind == session.KindAssistantText {
				replies++
			}
		}
		return replies
	}

	pc.echoPrompt("and udp?")
	read("after a send")
	if len(reader.Live) != 1 || reader.Live[0].ID != livePrompt || reader.Live[0].Text != "and udp?" {
		t.Fatalf("after a send: live %+v, want the prompt", reader.Live)
	}
	pane.rows = claudeScreen("explain tcp",
		"● TCP congestion control keeps a sender from overwhelming the network. Because",
		"  no one tells a sender how much capacity the path has, TCP infers it from")
	pc.catchUp(l)
	read("while it streams")
	want := "TCP congestion control keeps a sender from overwhelming the network. Because no one tells a sender how much capacity the path has, TCP infers it from"
	if len(reader.Live) != 2 || reader.Live[0].ID != liveReply || reader.Live[0].Text != want || reader.Live[0].TurnID == nil {
		t.Fatalf("while it streams: live %+v", reader.Live)
	}

	reply := `{"type":"assistant","uuid":"a1","parentUuid":"p1","timestamp":"2026-11-19T10:00:05Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"text","text":"TCP congestion control keeps a sender from **overwhelming** the network. Because no one tells a sender how much capacity the path has, TCP infers it from [loss](https://example.com)."}]}}` + "\n"
	next := `{"type":"user","uuid":"p2","parentUuid":"a1","timestamp":"2026-11-19T10:00:06Z","message":{"role":"user","content":"and udp?"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt+reply), 0o600); err != nil {
		t.Fatal(err)
	}
	pc.catchUp(l) // the screen still shows the block, which has landed
	if n := read("once the message landed"); n != 1 || slices.ContainsFunc(reader.Live, func(it session.Item) bool { return it.ID == liveReply }) {
		t.Errorf("once the message landed: %d replies, live %+v", n, reader.Live)
	}
	if err := os.WriteFile(path, []byte(prompt+reply+next), 0o600); err != nil {
		t.Fatal(err)
	}
	pc.catchUp(l) // a new turn, the last one's reply still on screen
	if n := read("once the prompt landed"); n != 1 || len(reader.Live) != 0 || len(reader.Content) != 3 {
		t.Errorf("once the prompt landed: %d replies, live %+v, %d items", n, reader.Live, len(reader.Content))
	}
}

// A block whose first row scrolled off the screen joins the rows read
// before where they overlap; one that overlaps nothing is not placed.
func TestStitch(t *testing.T) {
	before := []string{"a one", "b two", "", "c three", "d fo"}
	got := stitch(before, agent.Live{Rows: []string{"", "c three", "d four", "e five"}})
	if want := []string{"a one", "b two", "", "c three", "d four", "e five"}; !slices.Equal(got, want) {
		t.Errorf("stitch: %q, want %q", got, want)
	}
	if got := stitch(before, agent.Live{Rows: []string{"x", "y"}}); got != nil {
		t.Errorf("no overlap: %q", got)
	}
	if got := stitch(before, agent.Live{Rows: []string{"", ""}}); got != nil {
		t.Errorf("an overlap of blank rows: %q", got)
	}
	// Codex adds the blank row between paragraphs late.
	codex := []string{"p1 a", "p1 b", "p2 a", "p2 b", "p2 c"}
	got = stitch(codex, agent.Live{Rows: []string{"p1 b", "", "p2 a", "p2 b", "p2 c", "p2 d"}})
	if want := []string{"p1 a", "p1 b", "", "p2 a", "p2 b", "p2 c", "p2 d"}; !slices.Equal(got, want) {
		t.Errorf("a blank row added: %q, want %q", got, want)
	}
	if got := stitch(before, agent.Live{Top: true, Rows: []string{"new"}}); !slices.Equal(got, []string{"new"}) {
		t.Errorf("a new block: %q", got)
	}
}

// Claude Code ends a turn on its Stop hook before it writes the turn's
// last text block: the block keeps showing until it lands.
func TestPaneChatLiveAfterStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	prompt := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"sum up"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	l := paneLive{p: &gridPane{rows: claudeScreen("sum up", "● The directory holds two files.")}}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(l)
	pc.reduce(pc.tr.End(session.TurnCompleted)) // the Stop hook
	pc.catchUp(l)
	if len(pc.snap.Live) != 1 || pc.snap.Live[0].Text != "The directory holds two files." {
		t.Fatalf("after the hook ended the turn: live %+v", pc.snap.Live)
	}
	reply := `{"type":"assistant","uuid":"a1","parentUuid":"p1","timestamp":"2026-11-19T10:00:05Z","message":{"id":"m1","content":[{"type":"text","text":"The directory holds two files."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt+reply), 0o600); err != nil {
		t.Fatal(err)
	}
	pc.catchUp(l)
	if len(pc.snap.Live) != 0 || pc.snap.Content[len(pc.snap.Content)-1].Kind != session.KindAssistantText {
		t.Errorf("once the block landed: live %+v", pc.snap.Live)
	}
}

// A screen the chat cannot place its block on, as when Codex draws the
// reply's last rows over from further up, keeps the text read so far.
func TestPaneChatLiveKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	prompt := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"go"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	pane := &gridPane{rows: claudeScreen("go", "● First paragraph.", "", "  Second paragraph.")}
	l := paneLive{p: pane}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(l)
	rule := strings.Repeat("─", 80)
	pane.rows = []string{"  Some other rows", "  from further up.", "", rule, "❯ ", rule, "  footer"}
	pc.catchUp(l)
	if len(pc.snap.Live) != 1 || pc.snap.Live[0].Text != "First paragraph.\n\nSecond paragraph." {
		t.Errorf("live %+v, want the text read before", pc.snap.Live)
	}
}

// Two frames of Claude Code 2.1.330 streaming a reply, recorded 230 ms
// apart, with the block's first row off the screen in both: the second
// continues the first.
func TestStitchRecorded(t *testing.T) {
	frame := func(name string) agent.Live {
		b, err := os.ReadFile("../agent/testdata/live/" + name + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		return agent.LiveScreen(model.ProviderClaude, (&gridPane{rows: strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")}).Snapshot())
	}
	a, b := frame("claude-scrolled"), frame("claude-scrolled-next")
	got := stitch(a.Rows, b)
	if len(got) <= len(a.Rows) || got[0] != a.Rows[0] || !slices.Equal(got[len(got)-len(b.Rows):], b.Rows) {
		t.Errorf("stitch: %d rows from %d and %d: %q", len(got), len(a.Rows), len(b.Rows), got)
	}
}
