package agent

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Question is the current page of a terminal question, including its selection.
type Question struct {
	ID      string           `json:"id"`
	Prompt  string           `json:"prompt"`
	Options []QuestionOption `json:"options"`
	Multi   bool             `json:"multi"`
	Other   bool             `json:"other"`
	Submit  bool             `json:"submit"`

	Index       int               `json:"index,omitempty"`
	Total       int               `json:"total,omitempty"`
	Review      bool              `json:"review,omitempty"`
	Tabs        []QuestionTab     `json:"tabs,omitempty"`
	CanBack     bool              `json:"canBack,omitempty"`
	CanNext     bool              `json:"canNext,omitempty"`
	StepPrompt  string            `json:"-"`
	AllPrompts  []string          `json:"-"`
	ReviewAns   map[string]string `json:"-"`
	Cursor      int               `json:"-"`
	OtherIndex  int               `json:"-"`
	SubmitIndex int               `json:"-"`
	Notes       bool              `json:"-"`
	Draft       bool              `json:"-"`
	WriteIn     bool              `json:"-"`
}

// QuestionTab is one tab in a multi-question header bar.
type QuestionTab struct {
	Label    string `json:"label"`
	Answered bool   `json:"answered,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Selected    bool   `json:"selected"`
}

var (
	optionColumns = regexp.MustCompile(`\s{2,}`)
	previewColumn = regexp.MustCompile(`\s{2,}[┌│└├┬┴┼─].*$`)
	codexHeaderRe = regexp.MustCompile(`^Question\s+(\d+)/(\d+)(?:\s+\((\d+)\s+unanswered\))?`)
	agyHeaderRe   = regexp.MustCompile(`^Question\s+(\d+)/(\d+):\s*(.+)$`)
	ruleRowRe     = regexp.MustCompile(`^[─━-]{8,}$`)
)

// ReadQuestion reads Claude Code's, Codex's, or Antigravity's active terminal
// question page, never transcript lists.
func ReadQuestion(provider model.Provider, g vt.Grid) *Question {
	switch provider {
	case model.ProviderCodex:
		return readCodexQuestion(g)
	case model.ProviderAntigravity:
		return readAgyQuestion(g)
	case model.ProviderClaude:
		return readClaudeQuestion(g)
	default:
		return nil
	}
}

func readClaudeQuestion(g vt.Grid) *Question {
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
	if header >= 0 && end < 0 && reviewPage(strings.Join(rows[header:], "\n")) {
		end = len(rows)
	}
	if header < 0 || end < 0 {
		return nil
	}
	q := &Question{Cursor: -1, OtherIndex: -1, SubmitIndex: -1}
	headerLine := rows[header]
	q.Tabs = parseClaudeTabs(headerLine)
	if len(q.Tabs) > 0 {
		q.Total = len(q.Tabs)
	} else {
		q.Total = 1
	}
	q.CanBack = strings.Contains(headerLine, "←")
	q.CanNext = strings.Contains(headerLine, "→")

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
	q.StepPrompt = q.Prompt
	if reviewPage(strings.Join(rows[header:end], "\n")) {
		q.Review = true
		q.Index = q.Total
		q.CanNext = false
		q.ReviewAns = parseClaudeReview(rows[header+1 : first])
	}
	footer := ""
	if end < len(rows) {
		footer = strings.Join(rows[end:min(len(rows), end+2)], " ")
	}
	editingOther := strings.Contains(footer, "ctrl+g to edit")
	for oi, o := range opts {
		label := strings.TrimSpace(previewColumn.ReplaceAllString(o.label, ""))
		if label == "Chat about this" {
			break
		}
		selected := strings.HasPrefix(label, "[✔]") || strings.HasPrefix(label, "[✓]")
		if strings.HasPrefix(label, "[ ]") || selected {
			q.Multi = true
			label = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(label, "[ ]"), "[✔]"), "[✓]"))
		}
		i := len(q.Options)
		if strings.HasPrefix(label, "Type something") || (editingOther && o.cursor && oi == len(opts)-1) ||
			(editingOther && o.cursor && oi+1 < len(opts) && strings.TrimSpace(previewColumn.ReplaceAllString(opts[oi+1].label, "")) == "Chat about this") {
			q.Other = true
			q.OtherIndex = i
			if !strings.HasPrefix(label, "Type something") {
				q.Draft = true
			}
		} else {
			desc := claudeOptionDesc(rows, header+1+o.row, end)
			q.Options = append(q.Options, QuestionOption{Label: label, Description: desc, Selected: selected})
		}
		if o.cursor {
			q.Cursor = i
		}
	}
	submitIdx := len(q.Options)
	if q.Other {
		submitIdx++
	}
	for _, l := range rows[first:end] {
		t := strings.TrimSpace(l)
		cur := false
		if rest, ok := strings.CutPrefix(t, "❯"); ok {
			cur = true
			t = strings.TrimSpace(rest)
		}
		if t == "Submit" || t == "Next" {
			q.Submit = true
			q.SubmitIndex = submitIdx
			if cur {
				q.Cursor = submitIdx
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

func claudeOptionDesc(rows []string, optRow, end int) string {
	next := optRow + 1
	if next >= end {
		return ""
	}
	raw := strings.TrimSpace(previewColumn.ReplaceAllString(rows[next], ""))
	if raw == "" || optionLine.MatchString(rows[next]) || ruleRowRe.MatchString(raw) || raw == "Submit" || raw == "Next" ||
		strings.HasPrefix(raw, "❯") || raw == "Chat about this" || strings.HasPrefix(raw, "Notes:") {
		return ""
	}
	return raw
}

func parseClaudeTabs(line string) []QuestionTab {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "←")
	s = strings.TrimSuffix(s, "→")
	if i := strings.Index(s, "✔"); i >= 0 {
		s = s[:i]
	}
	var tabs []QuestionTab
	for s != "" {
		i := strings.IndexAny(s, "☐☒☑")
		if i < 0 {
			break
		}
		r := []rune(s[i:])[0]
		answered := r == '☒' || r == '☑'
		rest := s[i+len(string(r)):]
		next := strings.IndexAny(rest, "☐☒☑")
		label := rest
		if next >= 0 {
			label = rest[:next]
			s = rest[next:]
		} else {
			s = ""
		}
		if label = strings.TrimSpace(label); label != "" {
			tabs = append(tabs, QuestionTab{Label: label, Answered: answered})
		}
	}
	return tabs
}

func parseClaudeReview(rows []string) map[string]string {
	out := map[string]string{}
	curQ := ""
	for _, l := range rows {
		t := strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(t, "● "); ok {
			curQ = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(t, "→ "); ok && curQ != "" {
			out[curQ] = strings.TrimSpace(rest)
			curQ = ""
		}
	}
	return out
}

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
	q := &Question{Cursor: -1, OtherIndex: -1, SubmitIndex: -1, Other: true, Notes: strings.Contains(rows[end], "clear notes")}
	if m := codexHeaderRe.FindStringSubmatch(strings.TrimSpace(rows[header])); m != nil {
		cur, _ := strconv.Atoi(m[1])
		total, _ := strconv.Atoi(m[2])
		if cur >= 1 && total >= cur {
			q.Index = cur - 1
			q.Total = total
			q.CanBack = q.Index > 0
			q.CanNext = q.Index < q.Total-1
		}
	}
	if q.Total == 0 {
		q.Total = 1
	}
	q.Prompt = strings.TrimSpace(strings.Join(rows[header:header+1+opts[0].row], "\n"))
	q.StepPrompt = strings.TrimSpace(strings.Join(rows[header+1:header+1+opts[0].row], "\n"))
	if q.StepPrompt == "" {
		q.StepPrompt = q.Prompt
	}
	for i, o := range opts {
		parts := optionColumns.Split(o.label, 2)
		label := parts[0]
		desc := ""
		if len(parts) > 1 {
			desc = strings.TrimSpace(parts[1])
		}
		if strings.HasPrefix(label, "None of the above") {
			q.OtherIndex = i
		} else {
			q.Options = append(q.Options, QuestionOption{Label: label, Description: desc})
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

func readAgyQuestion(g vt.Grid) *Question {
	rows := screenRows(g)
	header, end := -1, -1
	var cur, total int
	var prompt string
	for y, l := range rows {
		if m := agyHeaderRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			c, _ := strconv.Atoi(m[1])
			tot, _ := strconv.Atoi(m[2])
			if c >= 1 && tot >= c {
				header, end = y, -1
				cur, total, prompt = c, tot, strings.TrimSpace(m[3])
			}
			continue
		}
		if header >= 0 && end < 0 {
			low := strings.ToLower(strings.TrimSpace(l))
			if strings.HasPrefix(low, "↑/↓ navigate") && (strings.Contains(low, "enter select") || strings.Contains(low, "enter submit")) {
				end = y
			} else if low == "enter submit · esc back" {
				end = y
			}
		}
	}
	if header < 0 || end < 0 {
		return nil
	}
	// A prompt below the footer means this question is old transcript, not active.
	for _, l := range rows[end+1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "> ") {
			return nil
		}
	}
	opts := formOptions(rows[header+1 : end])
	if len(opts) < 2 {
		return nil
	}
	q := &Question{
		Prompt:      prompt,
		StepPrompt:  prompt,
		Index:       cur - 1,
		Total:       total,
		Cursor:      -1,
		OtherIndex:  -1,
		SubmitIndex: -1,
	}
	footer := rows[end]
	if strings.EqualFold(strings.TrimSpace(footer), "enter submit · esc back") {
		for _, l := range rows[header+1 : end] {
			if strings.TrimSpace(l) == "Your answer:" {
				q.WriteIn = true
				break
			}
		}
	}
	q.CanBack = strings.Contains(footer, "← Back")
	q.CanNext = strings.Contains(footer, "→ Next")

	// Collect all question prompts printed above the Question divider ("? ...").
	var all []string
	seenDivider := false
	for y := header - 1; y >= 0; y-- {
		t := strings.TrimSpace(rows[y])
		if t == "" || t == "Question" || ruleRowRe.MatchString(t) {
			if ruleRowRe.MatchString(t) {
				seenDivider = true
			}
			continue
		}
		if rest, ok := strings.CutPrefix(t, "? "); ok && (seenDivider || len(all) > 0) {
			all = append(all, strings.TrimSpace(rest))
			continue
		}
		if len(all) > 0 {
			break
		}
		if seenDivider {
			break
		}
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) == total {
		q.AllPrompts = all
	}

	for _, o := range opts {
		label := o.label
		selected := strings.HasPrefix(label, "[x]") || strings.HasPrefix(label, "[X]") || strings.HasPrefix(label, "[✔]") || strings.HasPrefix(label, "[✓]")
		if strings.HasPrefix(label, "[ ]") || selected {
			q.Multi = true
			label = strings.TrimSpace(label[3:])
		}
		i := len(q.Options)
		if strings.HasPrefix(label, "Write-in") {
			q.Other = true
			q.OtherIndex = i
		} else {
			q.Options = append(q.Options, QuestionOption{Label: label, Selected: selected})
		}
		if o.cursor {
			q.Cursor = i
		}
	}
	if q.Multi && !q.WriteIn {
		q.Submit = true
	}
	if q.Cursor < 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(rows[header:end+1], "\n")))
	q.ID = fmt.Sprintf("%x", sum[:16])
	return q
}

// reviewPage is whether page is the review of a question's answers, which
// Claude Code draws without a menu hint when the pane is short; 2.1.330
// says only "Review your answers", earlier ones "Ready to submit your
// answers?" as well.
func reviewPage(page string) bool {
	return strings.Contains(page, "Ready to submit your answers?") || strings.Contains(page, "Review your answers") && strings.Contains(page, "Submit answers")
}
