package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// A prompt's pictures, which agy lists under media, come with it, and the
// "(image)" the daemon types for a picture sent alone does not.
func TestTranscriptMedia(t *testing.T) {
	pic := filepath.Join(t.TempDir(), "uploaded_media_1.png")
	os.WriteFile(pic, []byte("\x89PNG\r\n\x1a\n0000"), 0o600)
	line := func(text string, uris ...string) []byte {
		var media []map[string]string
		for _, u := range uris {
			media = append(media, map[string]string{"mime_type": "image/png", "uri": u})
		}
		b, _ := json.Marshal(map[string]any{"step_index": 0, "type": "USER_INPUT", "content": "<USER_REQUEST>\n" + text + "\n</USER_REQUEST>", "media": media})
		return b
	}
	prompt := func(es []session.Event) *session.Event {
		for i := range es {
			if es[i].Kind == session.KindUserPrompt {
				return &es[i]
			}
		}
		return nil
	}
	p := prompt(NewTranscript().Line(line("look at this", pic, "/gone.png")))
	if p == nil || p.Text != "look at this" || len(p.Attachments) != 1 || p.Attachments[0].MimeType != "image/png" || p.Attachments[0].Data == "" {
		t.Fatalf("prompt with a picture: %+v", p)
	}
	if p := prompt(NewTranscript().Line(line("(image)", pic))); p == nil || p.Text != "" || len(p.Attachments) != 1 {
		t.Errorf("a picture alone: %+v", p)
	}
	if p := prompt(NewTranscript().Line(line("(image)"))); p == nil || p.Text != "(image)" {
		t.Errorf("(image) typed without a picture stays: %+v", p)
	}
}
