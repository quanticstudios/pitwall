package settings

import (
	"slices"
	"strings"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
)

// slot is one chord of one action: the one being recorded. index -1 adds
// a chord.
type slot struct {
	action string
	table  string // "keys", "keys.tab" or "keys.pane"
	index  int
}

// tableOf is the config table a's key goes in.
func tableOf(a config.Action) string {
	switch {
	case a.Tab:
		return "keys.tab"
	case a.Pane:
		return "keys.pane"
	}
	return "keys"
}

func chordsOf(b *config.Bindings, action, table string) []config.Chord {
	switch table {
	case "keys.tab":
		return b.Tab[action]
	case "keys.pane":
		return b.Pane[action]
	}
	return b.Global[action]
}

func presetChords(preset, action, table string) []config.Chord {
	return chordsOf(config.Preset(preset), action, table)
}

// owner is the other action c already runs in s's section, or "".
func owner(b *config.Bindings, s slot, c config.Chord) string {
	e := key.Event{Name: c.Name, Modifiers: c.Mods}
	a := b.Action(e)
	switch s.table {
	case "keys.tab":
		a = b.TabAction(e)
	case "keys.pane":
		a = b.PaneModeAction(e)
	}
	if a == s.action {
		return ""
	}
	return a
}

// replaceChord is cs with chord i replaced by c (appended for -1), or
// removed when c is nil, without duplicates.
func replaceChord(cs []config.Chord, i int, c *config.Chord) []config.Chord {
	out := slices.Clone(cs)
	switch {
	case c == nil && i >= 0 && i < len(out):
		out = slices.Delete(out, i, i+1)
	case c != nil && i >= 0 && i < len(out):
		out[i] = *c
	case c != nil:
		out = append(out, *c)
	}
	var uniq []config.Chord
	for _, x := range out {
		if !slices.Contains(uniq, x) {
			uniq = append(uniq, x)
		}
	}
	if uniq == nil {
		uniq = []config.Chord{}
	}
	return uniq
}

// edit is one config write: an action's new chords.
type edit struct {
	action string
	table  string
	chords []config.Chord
}

// record is the writes for c recorded into s. With swap, the action that
// had c takes the chord s replaced (or just loses c when s adds one).
func record(b *config.Bindings, s slot, c config.Chord, swap bool) []edit {
	cur := chordsOf(b, s.action, s.table)
	out := []edit{{s.action, s.table, replaceChord(cur, s.index, &c)}}
	other := owner(b, s, c)
	if !swap || other == "" {
		return out
	}
	theirs := chordsOf(b, other, s.table)
	i := slices.Index(theirs, c)
	var give *config.Chord
	if s.index >= 0 && s.index < len(cur) {
		give = &cur[s.index]
	}
	return append(out, edit{other, s.table, replaceChord(theirs, i, give)})
}

// value is the TOML for e, nil when it matches the preset so the key can go.
func (e edit) value(preset string) *string {
	if slices.Equal(e.chords, presetChords(preset, e.action, e.table)) {
		return nil
	}
	v := config.BindingValue(e.chords)
	return &v
}

// matches reports whether every word of q is in one of texts.
func matches(q string, texts ...string) bool {
	all := strings.ToLower(strings.Join(texts, " "))
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(all, w) {
			return false
		}
	}
	return true
}

// firstSentence is a doc tag cut for a row label.
func firstSentence(doc string) string {
	if before, _, ok := strings.Cut(doc, ". "); ok {
		return before
	}
	return strings.TrimSuffix(doc, ".")
}
