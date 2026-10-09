package config

import (
	"strings"
	"unicode"
)

// queryAliases are the spellings a search may use for keys that a config
// does not take: other platforms' names and the macOS glyphs.
var queryAliases = map[string]string{
	"⌘": "cmd", "win": "super", "meta": "super",
	"^": "ctrl", "⌃": "ctrl",
	"option": "alt", "opt": "alt", "⌥": "alt",
	"⇧":      "shift",
	"pgdown": "pgdn",
	"↑":      "up", "↓": "down", "←": "left", "→": "right",
}

// queryWords splits a search the way people write keys: +, - and spaces
// separate, and each glyph is a word of its own, so "⌘⇧R" is three.
func queryWords(q string) []string {
	var out []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			out = append(out, strings.ToLower(w.String()))
			w.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '+' || r == '-' || unicode.IsSpace(r):
			flush()
		case strings.ContainsRune("⌘⌃⌥⇧^↑↓←→", r):
			flush()
			out = append(out, string(r))
		default:
			w.WriteRune(r)
		}
	}
	flush()
	return out
}

// KeyQuery reads a search as a chord: "ctrl+shift+d", "shift ctrl d",
// "⌘⇧R" and "opt-left" are chords, in any modifier order. ok is false
// unless every word is a modifier or a key, one at least is a modifier,
// and one is a key, so "tab", "d" and "command" stay words.
func KeyQuery(q string) (c Chord, ok bool) {
	for _, w := range queryWords(q) {
		if a, ok := queryAliases[w]; ok {
			w = a
		}
		if m, ok := parseMod(w); ok {
			c.Mods |= m
			continue
		}
		n, ok := parseKey(w)
		if !ok || c.Name != "" {
			return Chord{}, false
		}
		c.Name = n
	}
	if c.Mods == 0 || c.Name == "" {
		return Chord{}, false
	}
	return Unshift(c), true
}
