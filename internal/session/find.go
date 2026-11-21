package session

import (
	"encoding/json"
	"slices"
	"unicode"
)

// FindHit is a find's matches in item Item: the rune offset of each in
// FindText's text folded, which is Runes runes long.
type FindHit struct {
	Item  string
	At    []int
	Runes int
}

// FindText is the text a chat's find looks in, in it: a prompt, the
// assistant's text, its reasoning, or the plan an ExitPlanMode call
// proposes; "" for any other item.
func FindText(it *Item) string {
	switch it.Kind {
	case KindUserPrompt, KindAssistantText, KindReasoningText, KindReasoningSummaryText:
		return it.Text
	case KindToolCall:
		if it.Tool != nil && it.Tool.ToolName == "ExitPlanMode" {
			var in struct {
				Plan string `json:"plan"`
			}
			json.Unmarshal(it.Tool.Input, &in)
			return in.Plan
		}
	}
	return ""
}

// Fold is s with each rune lower-cased: as many runes as s, so a match's
// offsets in it are its offsets in s.
func Fold(s string) []rune {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return r
}

// FindRunes is the rune offset of each match of q in text, both folded,
// left to right, not overlapping.
func FindRunes(text, q []rune) []int {
	if len(q) == 0 {
		return nil
	}
	var out []int
	for i := 0; i+len(q) <= len(text); {
		if text[i] == q[0] && slices.Equal(text[i:i+len(q)], q) {
			out = append(out, i)
			i += len(q)
			continue
		}
		i++
	}
	return out
}

// Find is the matches of q, folded, in items' FindText, oldest item
// first: the newest items' up to limit matches, and whether it left
// older ones out. The newest item with a match is always in, however
// many it has.
func Find(items []Item, q []rune, limit int) ([]FindHit, bool) {
	var out []FindHit
	n := 0
	for i := len(items) - 1; i >= 0 && len(q) > 0; i-- {
		txt := FindText(&items[i])
		if txt == "" {
			continue
		}
		f := Fold(txt)
		at := FindRunes(f, q)
		if len(at) == 0 {
			continue
		}
		if n > 0 && n+len(at) > limit {
			slices.Reverse(out)
			return out, true
		}
		n += len(at)
		out = append(out, FindHit{Item: items[i].ID, At: at, Runes: len(f)})
	}
	slices.Reverse(out)
	return out, false
}

// Matches is how many matches hits hold.
func Matches(hits []FindHit) int {
	n := 0
	for _, h := range hits {
		n += len(h.At)
	}
	return n
}
