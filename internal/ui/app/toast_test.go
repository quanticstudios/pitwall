package app

import (
	"encoding/json"
	"testing"
)

// TestToastLine: a toast line is one line of plain ASCII that decodes back
// to the text, whatever the text holds.
func TestToastLine(t *testing.T) {
	title, body := "café ✳ \"quoted\"", "line\nbreak 🚀 \\"
	line := toastLine(title, body, toastTag("0123456789abcdef0123"))
	for i, c := range line {
		if c >= 0x80 || c == '\n' && i != len(line)-1 {
			t.Fatalf("byte %d of %q", i, line)
		}
	}
	var got struct{ Title, Body, Tag string }
	if err := json.Unmarshal(line, &got); err != nil || got.Title != title || got.Body != body || got.Tag != "0123456789abcdef" {
		t.Fatalf("%q decodes to %+v, %v", line, got, err)
	}
}
