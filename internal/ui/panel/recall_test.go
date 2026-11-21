package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Prompts keeps what the user typed, harness notes and the evidence line
// out and pasted text in place, and leaves out the replies and what
// another pane, agent or script sent.
func TestPrompts(t *testing.T) {
	img := session.Attachment{ID: "image-1", Kind: "image"}
	s := &session.Snapshot{Content: []session.Item{
		{ID: "1", Kind: session.KindUserPrompt, Text: "fix the bug<system-reminder>note</system-reminder>"},
		{ID: "2", Kind: session.KindAssistantText, Text: "done"},
		{ID: "3", Kind: session.KindUserPrompt, Text: "[pitwall: message from codex in #4]\nreview this"},
		{ID: "4", Kind: session.KindUserPrompt, Text: "look <pasted_content>\nlong paste\n</pasted_content> here", Attachments: []session.Attachment{img}},
		{ID: "5", Kind: session.KindUserPrompt, Text: "cut short", Cut: true},
	}}
	got := Prompts(s)
	if len(got) != 3 {
		t.Fatalf("prompts %+v", got)
	}
	if got[0].ID != "1" || got[0].Text != "fix the bug" {
		t.Errorf("harness note: %+v", got[0])
	}
	if got[1].Text != "look long paste here" || len(got[1].Attachments) != 1 {
		t.Errorf("paste: %+v", got[1])
	}
	if !got[2].Cut {
		t.Errorf("cut: %+v", got[2])
	}
	if Prompts(nil) != nil {
		t.Error("nil snapshot")
	}
}
