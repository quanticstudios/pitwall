package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// promptLines is a transcript of a prompt and its reply, chained after
// parent: the prompt's line is id, the reply's id+"r".
func promptLines(id, parent, text string) string {
	p := ""
	if parent != "" {
		p = `"parentUuid":"` + parent + `",`
	}
	return `{"type":"user",` + p + `"uuid":"` + id + `","timestamp":"2026-11-20T10:00:00Z","message":{"role":"user","content":"` + text + `"}}
{"type":"assistant","parentUuid":"` + id + `","uuid":"` + id + `r","timestamp":"2026-11-20T10:00:01Z","message":{"id":"m` + id + `","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok ` + text + `"}]}}
{"type":"system","subtype":"turn_duration","parentUuid":"` + id + `r","uuid":"` + id + `d","timestamp":"2026-11-20T10:00:02Z"}
`
}

// prompts are snap's prompt texts.
func prompts(snap session.Snapshot) []string {
	var out []string
	for _, it := range snap.Content {
		if it.Kind == session.KindUserPrompt {
			out = append(out, it.Text)
		}
	}
	return out
}

// fakeRewind makes p draw Claude Code's rewind picker for prompts on
// /rewind, its confirm menu on Enter, and the picked prompt back in the
// input box after Restore conversation, which two Esc clear.
func fakeRewind(p *fakePane, list []string) {
	idle := []string{"────────", "❯ ", "────────"}
	open, at, seen, escs := "", 0, 0, 0
	draw := func() {
		switch open {
		case "picker":
			rows := []string{"▔▔▔▔▔▔▔▔", "  Rewind", "  Restore the code and/or conversation to the point before…"}
			for i, l := range append(slices.Clone(list), "(current)") {
				cur := "    "
				if i == at {
					cur = "  ❯ "
				}
				rows = append(rows, cur+l)
				if i < len(list) {
					rows = append(rows, "    No code changes")
				}
			}
			p.screen = append(rows, "  Enter to continue · Esc to cancel")
		case "confirm":
			rows := []string{"▔▔▔▔▔▔▔▔", "  Rewind", "  Restore to the point before you sent this message:", "  │ " + list[at]}
			for i, o := range []string{"Restore conversation", "Summarize from here", "Never mind"} {
				cur := "    "
				if i == escs {
					cur = "  ❯ "
				}
				rows = append(rows, cur+string(rune('1'+i))+". "+o)
			}
			p.screen = rows
		case "draft":
			p.screen = []string{"────────", "❯ " + list[at], "────────"}
		default:
			p.screen = idle
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.screen = idle
	p.wrote = func() {
		in := string(p.input[seen:])
		seen = len(p.input)
		switch {
		case open == "" && strings.HasSuffix(string(p.input), "/rewind\r"):
			open, at = "picker", len(list)
		case open == "picker" && in == "\x1b[A":
			at = max(at-1, 0)
		case open == "picker" && in == "\r":
			open, escs = "confirm", 0
		case open == "confirm" && in == "\x1b[B":
			escs++
		case open == "confirm" && in == "\r":
			open, escs = "draft", 0
		case open == "draft" && in == "\x1b":
			if escs++; escs == 2 {
				open = ""
			}
		case in == "\x1b":
			open = ""
		}
		draw()
	}
}

func TestPaneRewind(t *testing.T) {
	d, f, _ := phoneDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := promptLines("p1", "", "fix it") + promptLines("p2", "p1d", "add tests") + promptLines("p3", "p2d", "ship it")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	snap, seq := paneRead(t, d, "a", session.Snapshot{}, 0)
	if got := prompts(snap); !slices.Equal(got, []string{"fix it", "add tests", "ship it"}) {
		t.Fatalf("prompts %q", got)
	}
	fakeRewind(f.pane(0), []string{"fix it", "add tests", "ship it"})
	before := len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatRewind{Chat: "a", Turn: "p1"}); r.(proto.ChatResult).Code == "" {
		t.Error("a rewind of the files too went through")
	}
	r, _ := d.chatRequest(context.Background(), proto.ChatRewind{Chat: "a", Turn: "p1", KeepFiles: true})
	if res := r.(proto.ChatResult); res.Code != "" {
		t.Fatalf("rewind: %+v", res)
	}
	want := "/rewind\r" + strings.Repeat("\x1b[A", 3) + "\r\r\x1b\x1b"
	if got := gotAfter(t, f.pane(0), before, len(want)); got != want {
		t.Errorf("typed %q, want %q", got, want)
	}
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.Content) != 0 {
		t.Errorf("after the rewind %q: want nothing", prompts(snap))
	}

	// Rewound to before p3 in the TUI itself: the next prompt branches off
	// p2's turn, and p3's turn goes.
	if err := os.WriteFile(path, []byte(body+promptLines("p4", "p2d", "try again")), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.paneChats = nil // read it from the start
	d.mu.Unlock()
	snap, _ = paneRead(t, d, "a", session.Snapshot{}, 0)
	if got := prompts(snap); !slices.Equal(got, []string{"fix it", "add tests", "try again"}) {
		t.Errorf("after a branch: %q", got)
	}
}

func TestRewindRank(t *testing.T) {
	later := []string{"fix it", "add tests", "fix it\nmore", "fix it now", "Fix the bug in parse.go and add logging"}
	for label, want := range map[string]int{"fix it": 2, "add tests": 1, "fix it now": 1, "fix…": 3, "Fix the bug in parse.go and add…": 1, "nothing": 0} {
		if got := rewindRank(later, label); got != want {
			t.Errorf("rank of %q: %d, want %d", label, got, want)
		}
	}
	p := func(s string) session.Item { return session.Item{Kind: session.KindUserPrompt, Text: s} }
	content := []session.Item{p("fix it"), {Kind: session.KindAssistantText, Text: "fix it"}, p("add tests"), p("fix it\nmore"), p("fix it now")}
	turn := "t2"
	content[2].TurnID = &turn
	if rewindTarget(content, "t2") != 2 || rewindTarget(content, "nope") != -1 || rewindTarget(content, "") != -1 {
		t.Error("rewindTarget")
	}
	// A prompt sent mid-turn shares its turn's id: its own id picks it.
	content[3].ID, content[3].TurnID = "mid", &turn
	if rewindTarget(content, "mid") != 3 || rewindTarget(content, "t2") != 2 {
		t.Error("rewindTarget of a mid-turn prompt")
	}
}

// Two prompts the picker cuts to one label: the walk cannot tell which is
// which, so it closes the picker and rewinds nothing rather than the newer
// one.
func TestPaneRewindCutLabels(t *testing.T) {
	d, f, _ := phoneDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := promptLines("p1", "", "Fix the bug in parse.go and add a test") + promptLines("p2", "p1d", "Fix the bug in parse.go and add logging")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	paneRead(t, d, "a", session.Snapshot{}, 0)
	cut := "Fix the bug in parse.go and add…"
	fakeRewind(f.pane(0), []string{cut, cut})
	before := len(f.pane(0).got())
	r, _ := d.chatRequest(context.Background(), proto.ChatRewind{Chat: "a", Turn: "p1", KeepFiles: true})
	if res := r.(proto.ChatResult); res.Code == "" || !strings.Contains(res.Message, "terminal") {
		t.Errorf("rewind of p1 under a shared label: %+v, want it refused", res)
	}
	want := "/rewind\r\x1b[A\x1b"
	if got := gotAfter(t, f.pane(0), before, len(want)); got != want {
		t.Errorf("typed %q, want %q: up to the newest match, then Esc", got, want)
	}
}
