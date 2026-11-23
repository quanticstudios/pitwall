package panel

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// promptMDText is a user message with bold, inline code, a list, a fenced
// code block, a link and an image token.
const promptMDText = "Fix the **resize** in `image.go` before [Image #1] lands:\n\n" +
	"1. keep the ratio\n2. cap at `2048` px\n\n" +
	"```go\nimg = fit(img, 2048)\n```\n\n" +
	"See [the spec](https://example.com/spec) for the rest."

// promptMDSnap is a chat of the user's markdown message, another agent's
// markdown message pitwall typed, and a plain message.
func promptMDSnap() *session.Snapshot {
	at := func(min int) string { return time.Date(2026, 11, 21, 14, min, 0, 0, time.UTC).Format(time.RFC3339Nano) }
	t1, t2, t3 := "t1", "t2", "t3"
	img := session.Attachment{ID: "image-1", Kind: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}
	return &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: promptMDText, TurnID: &t1, CreatedAt: at(1), UpdatedAt: at(1), Attachments: []session.Attachment{img}},
		{ID: "a1", Kind: session.KindAssistantText, Text: "Done: **resize** keeps the ratio.", TurnID: &t1, CreatedAt: at(2), UpdatedAt: at(2)},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "[pitwall: message from antigravity in #1.2; reply with pitwall send]\nReview **`fit`** please:\n- the ratio\n- the cap", TurnID: &t2, CreatedAt: at(3), UpdatedAt: at(3)},
		{ID: "a2", Kind: session.KindAssistantText, Text: "Reviewed.", TurnID: &t2, CreatedAt: at(4), UpdatedAt: at(4)},
		{ID: "p3", Kind: session.KindUserPrompt, Text: "1) foo and /home/u/foo_bar_baz.go:12\nthat's all, no markdown here", TurnID: &t3, CreatedAt: at(5), UpdatedAt: at(5)},
		{ID: "a3", Kind: session.KindAssistantText, Text: "OK.", TurnID: &t3, CreatedAt: at(6), UpdatedAt: at(6)},
	}}
}

// TestPromptMarkdownShots renders the user's and another agent's markdown
// messages in Ledger and Column to PITWALL_PROMPT_SHOTS for the PR.
func TestPromptMarkdownShots(t *testing.T) {
	dir := os.Getenv("PITWALL_PROMPT_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_PROMPT_SHOTS to render the PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	th := theme.Dark()
	for _, c := range []struct{ name, layout string }{
		{"prompt-md-ledger.png", LayoutLedger},
		{"prompt-md-column.png", LayoutColumn},
	} {
		img, _ := ledgerFrame(t, th, promptMDSnap(), c.layout, 1000, 1000)
		f, err := os.Create(filepath.Join(dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
