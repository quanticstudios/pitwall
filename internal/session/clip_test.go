package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClip: a long output keeps its head and tail at line ends with a
// count between, a long input string is cut inside its JSON, images lose
// their data, short and empty items pass untouched, and a cut never
// splits a rune.
func TestClip(t *testing.T) {
	var lines []string
	for i := range 5000 {
		lines = append(lines, strings.Repeat("x", 20)+" line "+string(rune('a'+i%26)))
	}
	out := strings.Join(lines, "\n")
	in, _ := json.Marshal(map[string]any{"file_path": "a.go", "content": out})
	it := Item{ID: "t", Kind: KindToolCall, Tool: &ToolCall{Output: out, Input: in},
		Attachments: []Attachment{{ID: "i", Kind: "image", Data: strings.Repeat("A", 1<<20)}}}
	c := Clip(it, 1024)
	if !c.Cut || len(c.Tool.Output) > 1100 || !strings.HasPrefix(c.Tool.Output, lines[0]+"\n") || !strings.HasSuffix(c.Tool.Output, "\n"+lines[4999]) {
		t.Fatalf("output %d bytes, cut %v:\n%.200s", len(c.Tool.Output), c.Cut, c.Tool.Output)
	}
	if !strings.Contains(c.Tool.Output, " more lines …\n") || strings.Count(c.Tool.Output, "\n") > 60 {
		t.Fatalf("no count line, or not cut at line ends: %q", c.Tool.Output)
	}
	var got map[string]string
	if err := json.Unmarshal(c.Tool.Input, &got); err != nil || got["file_path"] != "a.go" || len(got["content"]) > 1100 {
		t.Fatalf("input %v: %.200s", err, c.Tool.Input)
	}
	if c.Attachments[0].Data != "" || it.Attachments[0].Data == "" || it.Tool.Output != out {
		t.Fatal("Clip kept the image data, or changed the item it was given")
	}

	short := Item{ID: "s", Text: "hi", Tool: &ToolCall{Output: "ok"}}
	if c := Clip(short, 1024); c.Cut || c.Text != "hi" || c.Tool.Output != "ok" {
		t.Fatalf("short item: %+v", c)
	}
	if c := Clip(Item{}, 1024); c.Cut {
		t.Fatal("empty item cut")
	}
	wide := strings.Repeat("é", 2000) // no newlines, two bytes a rune
	if c := Clip(Item{Text: wide}, 1001); !utf8.ValidString(c.Text) || !strings.Contains(c.Text, "… 0 more lines …") {
		t.Fatalf("one long line: %q", c.Text)
	}
}
