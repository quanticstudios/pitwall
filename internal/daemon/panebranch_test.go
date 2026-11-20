package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// chain is a transcript of prompts, each chained after the line before it
// unless a prompt's parent is given: what a rewind leaves, the rewound
// turns kept in the file and the next prompt chained before them.
type chain struct {
	b    strings.Builder
	last string
	at   map[string]int64 // each prompt's line offset
}

// turn adds prompt id with text, chained after parent, or after the last
// line when parent is "".
func (c *chain) turn(id, parent, text string) {
	if parent == "" {
		parent = c.last
	}
	if c.at == nil {
		c.at = map[string]int64{}
	}
	c.at[id] = int64(c.b.Len())
	c.b.WriteString(promptLines(id, parent, text))
	c.last = id + "d"
}

// branchFile is p1, p2, p3 and p4 chained, then q chained after p2's turn:
// p3 and p4 were rewound.
func branchFile(t *testing.T) (string, *chain) {
	var c chain
	c.turn("p1", "", "one")
	c.turn("p2", "", "two")
	c.turn("p3", "", "three, rewound")
	c.turn("p4", "", "four, rewound")
	c.turn("q", "p2d", "two again")
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(c.b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, &c
}

func promptTexts(pc *paneChat) []string {
	var out []string
	for _, it := range pc.snap.Content {
		if it.Kind == session.KindUserPrompt {
			out = append(out, it.Text)
		}
	}
	return out
}

// A page that ends right before the prompt that branched off holds the
// rewound turns but not that prompt: the line after it says where the
// conversation went on from, and the page drops what came after that.
func TestPaneChatPageBranch(t *testing.T) {
	path, c := branchFile(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pc := &paneChat{id: "a", path: path, prov: "claude"}
	s, _, err := pc.reduceRange(f, 0, c.at["q"])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range s.Content {
		if strings.HasPrefix(it.ID, "p") && !strings.HasSuffix(it.ID, "r") {
			got = append(got, it.ID)
		}
	}
	if !slices.Equal(got, []string{"p1", "p2"}) {
		t.Errorf("page before q holds prompts %v, want p1 and p2", got)
	}
}

// A tail that starts after the branch's parent, as after a restart, still
// drops the rewound turns once the prompt that branched off comes.
func TestPaneChatTailBranch(t *testing.T) {
	path, c := branchFile(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	from := c.at["p3"] // the tail starts at a prompt line, after p2's turn
	pc := &paneChat{id: "a", path: path, prov: "claude", off: from, start: from, snap: session.Snapshot{SessionID: "a", Provider: "claude"}}
	pc.tr = pc.transcript()
	pc.lines(b[from:], "")
	if got := promptTexts(pc); !slices.Equal(got, []string{"two again"}) {
		t.Errorf("tail from p3 shows %q, want only q's prompt", got)
	}
}

// Only a prompt branches off: a page that ends before a tool result keeps
// everything, whatever line that result hangs off.
func TestPaneChatPageEndsBeforeResult(t *testing.T) {
	var c chain
	c.turn("p1", "", "one")
	c.turn("p2", "", "two")
	cut := int64(c.b.Len())
	c.b.WriteString(`{"type":"user","parentUuid":"p1r","uuid":"x1","timestamp":"2026-11-20T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t0","content":"ok"}]}}` + "\n")
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(c.b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pc := &paneChat{id: "a", path: path, prov: "claude"}
	s, _, err := pc.reduceRange(f, 0, cut)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Content); n != 4 {
		t.Errorf("page holds %d items, want both turns' 4", n)
	}
}
