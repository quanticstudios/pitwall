package chatin

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
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
	if m[1] == "gpt" || m[1] == "gemini" {
		name := "GPT-" + version
		if m[1] == "gemini" {
			name = "Gemini " + version
		}
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

// Antigravity CLI's choices, as agy 1.5.13 takes them in its TUI (/model,
// /effort, Shift+Tab) and over ACP (its model, thought_level and mode
// options): model families without the effort suffix `agy models` adds.
// shortcut: a fixed list of agy's models; read them from agy if they change often.
var (
	agyModels  = []string{"gemini-4-neon", "gemini-4-carbon", "gemini-4-argon", "gemini-4-hydrogen", "gemini-3.8-flash", "claude-opus-5-5", "claude-sonnet-5-5"}
	agyEfforts = []string{"low", "medium", "high", "xhigh"}
	agyModes   = []string{ModeDefault, "accept-edits", "auto", ModePlan}
)

// ModelsOf are the models the model chip offers for provider p's chats.
func ModelsOf(p model.Provider) []string {
	if p == model.ProviderAntigravity {
		return agyModels
	}
	return remote.Models
}

// EffortsOf are the efforts the effort chip offers for p's chats; nil for
// an agent whose efforts pitwall does not know, which shows no chip.
func EffortsOf(p model.Provider) []string {
	switch p {
	case model.ProviderClaude:
		return remote.Efforts
	case model.ProviderAntigravity:
		return agyEfforts
	}
	return nil
}

// ModeItemsOf is ModeItems for p's chats: Antigravity's own four modes.
func ModeItemsOf(p model.Provider, bypass bool) []string {
	if p == model.ProviderAntigravity {
		return agyModes
	}
	return ModeItems(bypass)
}

// NextModeOf is NextMode along ModeItemsOf(p, bypass).
func NextModeOf(p model.Provider, m string, bypass bool) string {
	items := ModeItemsOf(p, bypass)
	for i, x := range items {
		if x == m {
			return items[(i+1)%len(items)]
		}
	}
	return items[0]
}

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
	case "accept-edits":
		return "Accept edits"
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

// meterWindow is the context window when the window is known but the
// current context token count is unknown (as right after a compaction
// that did not report post-compaction tokens).
func meterWindow(u *session.Usage) (int64, bool) {
	if u == nil || u.ContextTokens != nil || u.MaxTokens == nil || *u.MaxTokens <= 0 {
		return 0, false
	}
	return *u.MaxTokens, true
}

// MeterLevel is how the context meter looks: calm, a warning, or danger.
type MeterLevel uint8

const (
	MeterOK MeterLevel = iota
	MeterWarning
	MeterDanger
)

// autoCompactPct is where Claude Code compacts the conversation, in
// percent of the window.
// shortcut: Claude Code reports no threshold; 92% is close to where 2.1.x compacts. Read the TUI's own reading if it drifts.
const autoCompactPct = 92

// AutoCompactAt is the tokens in context at which Claude Code compacts a
// window of total.
func AutoCompactAt(total int64) int64 { return total * autoCompactPct / 100 }

// Percent is used out of total in whole percent, rounded down so 99.6%
// never reads 100%, and kept to 0 through 100.
func Percent(used, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(max(0, min(100, used*100/total)))
}

// Level is the meter's look at used of total: a warning from 75%, danger
// within 5 points of the auto-compact point.
func Level(used, total int64) MeterLevel {
	switch {
	case total <= 0:
		return MeterOK
	case used*100 >= total*(autoCompactPct-5):
		return MeterDanger
	case used*4 >= total*3:
		return MeterWarning
	}
	return MeterOK
}

// Tokens is n as the meter writes it: 950, 84.2k, 200k, 1M, 1.5M.
func Tokens(n int64) string {
	one := func(v float64, unit string) string {
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + unit
	}
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_950:
		return one(float64(n)/1e3, "k")
	}
	return one(float64(n)/1e6, "M")
}

// Pending reports whether s waits on the user: an approval, a question or
// a plan.
func Pending(s *session.Snapshot) bool {
	return len(s.PendingApprovals) > 0 || len(s.PendingUserInputs) > 0 || s.PlanApproval != nil
}

// asking reports whether s waits on a question alone, which a typed
// draft answers.
func asking(s *session.Snapshot) bool {
	return len(s.PendingUserInputs) > 0 && len(s.PendingApprovals) == 0 && s.PlanApproval == nil
}

// asked is the newest question s waits on, "" for none.
func asked(s *session.Snapshot) string {
	if n := len(s.PendingUserInputs); n > 0 {
		return s.PendingUserInputs[n-1].ID
	}
	return ""
}

// SlashCommand is whether text is a slash command, as /compact, not a path
// such as /usr/bin, which may well be an answer.
func SlashCommand(text string) bool {
	w, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	return len(w) > 1 && w[0] == '/' && !strings.Contains(w[1:], "/")
}

// Placeholder is the empty box's text for a chat with s.
func Placeholder(s *session.Snapshot) string {
	switch {
	case asking(s):
		return "Pick an answer above, or type your own"
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

// Slash is the /word the caret (in runes) is in, for the slash-command
// menu: the rune index of its / and the whole word. The / starts the
// text or follows whitespace, so and/or and a URL are none; ok false
// otherwise.
func Slash(text string, caret int) (start int, word string, ok bool) {
	r := []rune(text)
	if caret < 1 || caret > len(r) {
		return 0, "", false
	}
	start = caret
	for start > 0 && !unicode.IsSpace(r[start-1]) {
		start--
	}
	if start == caret || r[start] != '/' {
		return 0, "", false
	}
	end := caret
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	return start, string(r[start:end]), true
}
