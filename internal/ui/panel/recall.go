package panel

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// Prompts is s's prompts that the user typed, oldest first, as the
// composer recalls them: harness notes and the evidence line out, pasted
// text back in place, and none that another pane, an agent or a script
// sent through pitwall.
func Prompts(s *session.Snapshot) []chatin.Sent {
	if s == nil {
		return nil
	}
	var out []chatin.Sent
	for _, it := range s.Content {
		if it.Kind != session.KindUserPrompt {
			continue
		}
		text := harnessTag.ReplaceAllString(it.Text, "")
		text = pastedContent.ReplaceAllString(text, "$1")
		text = strings.TrimSpace(evidenceTail.ReplaceAllString(text, ""))
		if pitwallHeader.MatchString(text) {
			continue
		}
		out = append(out, chatin.Sent{ID: it.ID, Text: text, Attachments: it.Attachments, Cut: it.Cut})
	}
	return out
}
