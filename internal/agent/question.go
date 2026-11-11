package agent

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Question is the current page of a terminal question, including its selection.
type Question struct {
	ID         string           `json:"id"`
	Prompt     string           `json:"prompt"`
	Options    []QuestionOption `json:"options"`
	Multi      bool             `json:"multi"`
	Other      bool             `json:"other"`
	Submit     bool             `json:"submit"`
	Cursor     int              `json:"-"`
	OtherIndex int              `json:"-"`
	Notes      bool             `json:"-"`
	Draft      bool             `json:"-"`
}

type QuestionOption struct {
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

// ReadQuestion reads Claude Code's AskUserQuestion page, never transcript lists.
func ReadQuestion(provider model.Provider, g vt.Grid) *Question {
	if provider == model.ProviderCodex {
		return readCodexQuestion(g)
	}
	if provider != model.ProviderClaude {
		return nil
	}
	rows := screenRows(g)
	header, end := -1, -1
	for y, l := range rows {
		if strings.ContainsAny(l, "☐☒☑") {
			header = y
		}
		if header >= 0 && menuHint(l) {
			end = y
			break
		}
	}
	if header >= 0 && end < 0 && strings.Contains(strings.Join(rows[header:], "\n"), "Ready to submit your answers?") {
		end = len(rows)
	}
	if header < 0 || end < 0 {
		return nil
	}
	q := &Question{Cursor: -1, OtherIndex: -1}
	opts := formOptions(rows[header+1 : end])
	if len(opts) < 2 {
		return nil
	}
	for _, l := range rows[header+1+opts[len(opts)-1].row+1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "❯") || strings.HasPrefix(t, "›") {
			return nil
		}
	}
	first := header + 1 + opts[0].row
	q.Prompt = strings.TrimSpace(strings.Join(rows[header+1:first], "\n"))
	if q.Prompt == "" {
		return nil
	}
	for _, o := range opts {
		label := o.label
		if label == "Chat about this" {
			break
		}
		selected := strings.HasPrefix(label, "[✔]") || strings.HasPrefix(label, "[✓]")
		if strings.HasPrefix(label, "[ ]") || selected {
			q.Multi = true
			label = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(label, "[ ]"), "[✔]"), "[✓]"))
		}
		i := len(q.Options)
		if strings.HasPrefix(label, "Type something") {
			q.Other = true
			q.OtherIndex = i
		} else {
			q.Options = append(q.Options, QuestionOption{Label: label, Selected: selected})
		}
		if o.cursor {
			q.Cursor = i
		}
	}
	for _, l := range rows[first:end] {
		if strings.TrimSpace(l) == "Submit" || strings.TrimSpace(l) == "❯    Submit" {
			q.Submit = true
			if strings.HasPrefix(strings.TrimSpace(l), "❯") {
				q.Cursor = len(q.Options) + 1
			}
		}
	}
	if q.Cursor < 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(rows[header:end], "\n")))
	q.ID = fmt.Sprintf("%x", sum[:16])
	return q
}

var optionColumns = regexp.MustCompile(`\s{2,}`)

func readCodexQuestion(g vt.Grid) *Question {
	rows := screenRows(g)
	header, end := -1, -1
	for y, l := range rows {
		if strings.HasPrefix(strings.TrimSpace(l), "Question ") {
			header = y
		}
		if header >= 0 && strings.Contains(l, "enter to submit") {
			end = y
			break
		}
	}
	if header < 0 || end < 0 {
		return nil
	}
	opts := formOptions(rows[header+1 : end])
	if len(opts) < 2 {
		return nil
	}
	for _, l := range rows[end+1:] {
		if strings.HasPrefix(strings.TrimSpace(l), "›") {
			return nil
		}
	}
	q := &Question{Cursor: -1, OtherIndex: -1, Other: true, Notes: strings.Contains(rows[end], "clear notes")}
	q.Prompt = strings.TrimSpace(strings.Join(rows[header:header+1+opts[0].row], "\n"))
	for i, o := range opts {
		label := optionColumns.Split(o.label, 2)[0]
		if strings.HasPrefix(label, "None of the above") {
			q.OtherIndex = i
		} else {
			q.Options = append(q.Options, QuestionOption{Label: label})
		}
		if o.cursor {
			q.Cursor = i
		}
	}
	if q.Notes {
		for _, l := range rows[header:end] {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "› ") && optionLine.FindStringSubmatch(l) == nil && t != "› Add notes" {
				q.Draft = true
			}
		}
	}
	if q.Cursor < 0 || q.OtherIndex < 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(rows[header:end+1], "\n")))
	q.ID = fmt.Sprintf("%x", sum[:16])
	return q
}
