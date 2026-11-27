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
		reader.Live = noStatus(reader.Live)
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

// noStatus is items without the spinner row's, which these screens all draw.
func noStatus(items []session.Item) []session.Item {
	return slices.DeleteFunc(slices.Clone(items), func(it session.Item) bool { return it.ID == liveStatus })
}

// A block whose first row scrolled off the screen joins the rows read
// before where they overlap, its cells with it; one that overlaps nothing
// is not placed.
func TestStitch(t *testing.T) {
	live := func(top bool, rows ...string) agent.Live {
		lv := agent.Live{Top: top, Rows: rows}
		for _, r := range rows {
			lv.Cells = append(lv.Cells, []vt.Cell{{Content: r}})
		}
		return lv
	}
	check := func(name string, rows []string, cells [][]vt.Cell, want []string) {
		t.Helper()
		if !slices.Equal(rows, want) || len(cells) != len(rows) {
			t.Errorf("%s: %q, %d cells, want %q", name, rows, len(cells), want)
		}
		for i := range min(len(rows), len(cells)) {
			if cells[i][0].Content != rows[i] {
				t.Errorf("%s: row %d is %q, its cells %q", name, i, rows[i], cells[i][0].Content)
			}
		}
	}
	b := live(false, "a one", "b two", "", "c three", "d fo")
	rows, cells := stitch(b.Rows, b.Cells, live(false, "", "c three", "d four", "e five"))
	check("stitch", rows, cells, []string{"a one", "b two", "", "c three", "d four", "e five"})
	if rows, _ := stitch(b.Rows, b.Cells, live(false, "x", "y")); rows != nil {
		t.Errorf("no overlap: %q", rows)
	}
	if rows, _ := stitch(b.Rows, b.Cells, live(false, "", "")); rows != nil {
		t.Errorf("an overlap of blank rows: %q", rows)
	}
	// Codex adds the blank row between paragraphs late.
	c := live(false, "p1 a", "p1 b", "p2 a", "p2 b", "p2 c")
	rows, cells = stitch(c.Rows, c.Cells, live(false, "p1 b", "", "p2 a", "p2 b", "p2 c", "p2 d"))
	check("a blank row added", rows, cells, []string{"p1 a", "p1 b", "", "p2 a", "p2 b", "p2 c", "p2 d"})
	rows, cells = stitch(b.Rows, b.Cells, live(true, "new"))
	check("a new block", rows, cells, []string{"new"})
}

// The reply shows as the markdown Claude Code draws, and that markdown,
// its link's text a guess at one word, finds the transcript's message.
func TestPaneChatLiveMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	prompt := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"docs?"}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	const b, x, code = "\x1b[1m", "\x1b[0m", "\x1b[38;2;177;185;249m"
	link := "\x1b]8;;https://example.com/docs\x1b\\https://example.com/docs\x1b]8;;\x1b\\"
	pane := &gridPane{rows: claudeScreen("docs?", "● Read the setup docs ("+link+") and run "+code+"make"+x+" "+b+"now"+x+".", "", "  - one", "  - two")}
	l := paneLive{p: pane}
	pc := &paneChat{id: "a", path: path, prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(l)
	want := "Read the setup [docs](https://example.com/docs) and run `make` **now**.\n\n- one\n- two"
	if live := noStatus(pc.snap.Live); len(live) != 1 || live[0].Text != want {
		t.Fatalf("live %+v, want %q", live, want)
	}
	reply := `{"type":"assistant","uuid":"a1","parentUuid":"p1","timestamp":"2026-11-19T10:00:05Z","message":{"id":"m1","content":[{"type":"text","text":"Read the [setup docs](https://example.com/docs) and run ` + "`make`" + ` **now**.\n\n- one\n- two"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(prompt+reply), 0o600); err != nil {
		t.Fatal(err)
	}
	pc.catchUp(l)
	if live := noStatus(pc.snap.Live); len(live) != 0 {
		t.Errorf("once the message landed: live %+v", live)
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
	got, cells := stitch(a.Rows, a.Cells, b)
	if len(got) <= len(a.Rows) || len(cells) != len(got) || got[0] != a.Rows[0] || !slices.Equal(got[len(got)-len(b.Rows):], b.Rows) {
		t.Errorf("stitch: %d rows from %d and %d: %q", len(got), len(a.Rows), len(b.Rows), got)
	}
}
