package chatin

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// modelAliases names Claude Code's moving family aliases.
var modelAliases = map[string]string{
	"default": "Default",
	"fable":   "Fable 5.2",
	"opus":    "Opus 5.5",
	"sonnet":  "Sonnet 5.5",
	"haiku":   "Haiku 5.5",
}

var modelVersion = regexp.MustCompile(`^([a-z]+)-([0-9]+)(?:[.-]([0-9]+))?(?:-([a-z0-9-]+))?$`)

// ModelName reads the family and version from an id, leaving unknown ids intact.
func ModelName(id string) string {
	wide := ""
	if strings.HasSuffix(strings.ToLower(id), "[1m]") {
		id, wide = id[:len(id)-4], " 1M"
	}
	if name, ok := modelAliases[strings.ToLower(id)]; ok {
		return name + wide
	}
	short := id
	if strings.HasPrefix(strings.ToLower(short), "claude-") {
		short = short[len("claude-"):]
	}
	m := modelVersion.FindStringSubmatch(strings.ToLower(short))
	if m == nil {
		return short + wide
	}
	version := m[2]
	if m[3] != "" && len(m[3]) != 8 { // a date suffix is not a minor version
		version += "." + m[3]
	}
	if m[1] == "gpt" {
		name := "GPT-" + version
		for _, word := range strings.Split(m[4], "-") {
			if word != "" {
				name += " " + strings.ToUpper(word[:1]) + word[1:]
			}
		}
		return name + wide
	}
	return strings.ToUpper(m[1][:1]) + m[1][1:] + " " + version + wide
}

// The permission modes the composer's pill cycles through, in order
// (Claude Code's --permission-mode).
const (
	ModeDefault = "default"
	ModeAccept  = "acceptEdits"
	ModePlan    = "plan"
	ModeBypass  = "bypassPermissions"
)

var modes = []string{ModeDefault, ModeAccept, ModePlan}

// ModeOf is the permission mode s runs in.
func ModeOf(s *session.Snapshot) string {
	switch {
	case s.InteractionMode == "plan":
		return ModePlan
	case s.PermissionMode == "":
		return ModeDefault
	}
	return s.PermissionMode
}

// NextMode is the mode Shift+Tab moves to from m: the next of Default,
// Accept edits, Plan and, when bypass, Bypass permissions; Default from
// any other.
func NextMode(m string, bypass bool) string {
	ms := ModeItems(bypass)
	for i, x := range ms {
		if x == m {
			return ms[(i+1)%len(ms)]
		}
	}
	return ModeDefault
}

// ModeItems are the modes the mode chip's picker lists, in Shift+Tab's
// order: bypass permissions only when the chat may enter it.
func ModeItems(bypass bool) []string {
	if bypass {
		return append(modes[:len(modes):len(modes)], ModeBypass)
	}
	return modes
}

// ModeName is the pill's label for m.
func ModeName(m string) string {
	switch m {
	case ModeDefault:
		return "Default"
	case ModeAccept:
		return "Accept edits"
	case ModePlan:
		return "Plan"
	case ModeBypass:
		return "Bypass permissions"
	case "auto":
		return "Auto"
	}
	return m
}

// EffortName is the effort chip's label for e: "High", or "Effort" while
// it is unset or the default.
func EffortName(e string) string {
	switch e {
	case "", "default":
		return "Effort"
	case "xhigh":
		return "Extra high"
	}
	return strings.ToUpper(e[:1]) + e[1:]
}

// Meter is the context meter's reading from u: the tokens in context and
// the window's size, ok false when either is unknown.
func Meter(u *session.Usage) (used, total int64, ok bool) {
	if u == nil || u.ContextTokens == nil || u.MaxTokens == nil || *u.MaxTokens <= 0 {
		return 0, 0, false
	}
	return *u.ContextTokens, *u.MaxTokens, true
}

// MeterWarn reports whether the meter turns yellow: at 75% full.
func MeterWarn(used, total int64) bool { return total > 0 && used*4 >= total*3 }

// Tokens is n as the meter writes it: 950, 62K, 1M, 1.5M.
func Tokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_500:
		return strconv.FormatInt((n+500)/1000, 10) + "K"
	}
	m := strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64)
	return strings.TrimSuffix(m, ".0") + "M"
}

// Pending reports whether s waits on the user: an approval, a question or
// a plan.
func Pending(s *session.Snapshot) bool {
	return len(s.PendingApprovals) > 0 || len(s.PendingUserInputs) > 0 || s.PlanApproval != nil
}

// Placeholder is the empty box's text for a chat with s.
func Placeholder(s *session.Snapshot) string {
	switch {
	case Pending(s):
		return "Answer above, or type to queue"
	case s.Busy():
		return "Type to queue a follow-up"
	}
	name := "Claude"
	if p := string(s.Provider); p != "" && p != "claude" {
		name = strings.ToUpper(p[:1]) + p[1:]
	}
	return "Reply to " + name
}

// Slash is the command the text starts with while the caret (in runes)
// is in its first word, for the slash-command menu; ok false otherwise.
func Slash(text string, caret int) (word string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	word = text
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		word = text[:i]
	}
	if caret < 1 || caret > len([]rune(word)) {
		return "", false
	}
	return word, true
}
