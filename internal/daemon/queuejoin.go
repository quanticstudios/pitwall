package daemon

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// When a turn ends, what the chat queued goes out as one prompt: the
// messages in order, a blank line between them. A slash command or a !
// shell command goes alone, in its place, since an agent reads one only
// at the start of a prompt; so does a message marked Send separately.

// command reports whether text is a slash command or a ! shell command.
func command(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "/") || strings.HasPrefix(t, "!")
}

// joins is how many of texts, from the first, go in one prompt: the
// first alone when it is a command or alone[0], else it and the plain
// messages right after it.
func joins(texts []string, alone []bool) int {
	n := 0
	for i, t := range texts {
		if command(t) || alone[i] {
			if n == 0 {
				return 1
			}
			break
		}
		n++
	}
	return n
}

// tokenNo is an [Image #N] token's number.
var tokenNo = regexp.MustCompile(`\[Image #([0-9]+)\]`)

// joinPrompts joins texts with a blank line between them. Each one's
// [Image #N] tokens are numbered on past the ones before it, images[i]
// being how many images text i carries, so the n-th token still names
// the n-th image of the joined attachments.
func joinPrompts(texts []string, images []int) string {
	var b strings.Builder
	off := 0
	for i, t := range texts {
		top := 0
		t = tokenNo.ReplaceAllStringFunc(t, func(tok string) string {
			n, _ := strconv.Atoi(tokenNo.FindStringSubmatch(tok)[1])
			top = max(top, n)
			return "[Image #" + strconv.Itoa(n+off) + "]"
		})
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(t))
		off += max(top, images[i])
	}
	return b.String()
}

// joinAttachments is as followed by more, each id kept unique, since
// each message numbers its own (image-1, image-2).
func joinAttachments(as, more []session.Attachment) []session.Attachment {
	for _, a := range more {
		id := a.ID
		for n := 2; slices.ContainsFunc(as, func(o session.Attachment) bool { return o.ID == a.ID }); n++ {
			a.ID = id + "-" + strconv.Itoa(n)
		}
		as = append(as, a)
	}
	return as
}

// imageCount is how many of as are images.
func imageCount(as []session.Attachment) int {
	n := 0
	for _, a := range as {
		if a.Kind == "image" {
			n++
		}
	}
	return n
}

// Why a queue waits once the turn ended, as the strip above it says.
const (
	waitAnswer   = "Waiting for your answer"
	waitApproval = "Waiting for your approval"
	waitFailed   = "The last turn failed"
	waitStart    = "Waiting for the agent to start"
	// mayHaveSent is a queued message's Error when pitwall stopped
	// between handing it to the agent and seeing it arrive.
	mayHaveSent = "pitwall stopped while sending this, so it may have been sent: Retry sends it again"
)

// waitLimit is the reason a usage limit gives until it resets at at.
func waitLimit(at time.Time) string {
	f := "15:04"
	if time.Until(at) > 20*time.Hour {
		f = "Mon 15:04"
	}
	return "Waiting for the usage limit to reset at " + at.Local().Format(f)
}
