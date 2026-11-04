package agent

import (
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Adapted from tuios (MIT): internal/harness/draft.go, internal/harness/region.go
// and the [prompt_box] blocks of its Claude Code and Codex manifests.
//
// Draft reports whether the input box on screen g of agent p holds text. It
// is false for an agent whose box pitwall cannot read and for a screen that
// shows no box, so those panes keep only the pause after the user's last key.
func Draft(p model.Provider, g vt.Grid) bool {
	raw, shown := boxRows(g)
	switch p {
	case model.ProviderClaude:
		return claudeDraft(raw, shown)
	case model.ProviderCodex:
		return codexDraft(raw, shown)
	}
	return false
}

// boxRows returns g's rows top to bottom, and the same rows with faint and
// reverse cells blank. A placeholder or a suggested prompt is drawn faint,
// with the cursor on its first letter drawn reverse; the user's text is not.
func boxRows(g vt.Grid) (raw, shown []string) {
	var r, s strings.Builder
	for y := range g.Rows {
		r.Reset()
		s.Reset()
		for x := range g.Cols {
			c := g.At(x, y)
			switch {
			case c.Width == 0:
			case c.Content == "":
				r.WriteByte(' ')
				s.WriteByte(' ')
			case c.Attrs&(vt.Faint|vt.Reverse) != 0:
				r.WriteString(c.Content)
				s.WriteByte(' ')
			default:
				r.WriteString(c.Content)
				s.WriteString(c.Content)
			}
		}
		raw, shown = append(raw, r.String()), append(shown, s.String())
	}
	return raw, shown
}

// claudeDraft reads the box Claude Code draws between its last two rules,
// which opens with ❯, or with ! in shell mode.
func claudeDraft(raw, shown []string) bool {
	top, bottom := -1, -1
	for i := len(raw) - 1; i >= 0 && top < 0; i-- {
		if !isRule(raw[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
		} else {
			top = i
		}
	}
	if top < 0 {
		return false
	}
	box := strings.TrimSpace(strings.Join(raw[top+1:bottom], "\n"))
	if !strings.HasPrefix(box, "❯") && !strings.HasPrefix(box, "!") {
		return false
	}
	text := strings.TrimSpace(strings.Join(shown[top+1:bottom], "\n"))
	return strings.TrimSpace(strings.TrimPrefix(text, "❯")) != ""
}

// isRule reports whether a row is the box's border: dashes from the left edge
// to the row's end, with at most a session's name between them. Claude indents
// a draft's later rows, so a rule-like line typed in a draft is no border.
func isRule(row string) bool {
	row = strings.TrimRight(row, " ")
	return (strings.HasPrefix(row, "───") || strings.HasPrefix(row, "━━━")) &&
		(strings.HasSuffix(row, "─") || strings.HasSuffix(row, "━"))
}

// codexDraft reads the box Codex opens with › at the left edge, or » at Ultra
// effort, and ! in shell mode. Its last such row is the box, as the transcript
// above repeats the glyph. The rows from there down to the footer, the last
// block of rows after a blank one, are the box's, and any text in them counts.
func codexDraft(raw, shown []string) bool {
	i := len(raw) - 1
	for i >= 0 && !codexGlyph(raw[i]) {
		i--
	}
	if i < 0 {
		return false
	}
	if strings.HasPrefix(raw[i], "!") || !blank(strings.TrimLeft(shown[i], "›»")) {
		return true
	}
	end := len(raw)
	for end > i+1 && blank(raw[end-1]) {
		end--
	}
	foot := end
	for foot > i+1 && !blank(raw[foot-1]) {
		foot--
	}
	if foot == i+1 { // no blank row under the glyph: these rows are the box's
		foot = end
	}
	return slices.ContainsFunc(shown[i+1:foot], func(row string) bool { return !blank(row) })
}

func blank(row string) bool { return strings.TrimSpace(row) == "" }

func codexGlyph(row string) bool {
	row = strings.TrimRight(row, " ")
	return strings.HasPrefix(row, "›") || strings.HasPrefix(row, "»") || row == "!" || strings.HasPrefix(row, "! ")
}
