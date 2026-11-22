package daemon

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// fakeStepTUI simulates Claude Code, Codex or Antigravity multi-question TUI
// screens (matching the recorded real fixtures) on a fakePane.
type fakeStepTUI struct {
	p       *fakePane
	prov    model.Provider
	qs      []session.Question
	page    int // 0..len(qs)-1, len(qs) for Claude review, -1 when submitted/closed
	cursor  int
	ticks   map[int]map[int]bool // page -> option -> checked
	ans     []string             // per page, chosen answer
	done    []bool
	notes   bool   // Codex notes open
	writeIn bool   // Antigravity Write-in open
	buf     string // typed text in Other/notes/Write-in
	read    int
	review  bool
}

func newFakeStepTUI(p *fakePane, prov model.Provider, qs []session.Question) *fakeStepTUI {
	f := &fakeStepTUI{
		p:      p,
		prov:   prov,
		qs:     qs,
		ticks:  map[int]map[int]bool{},
		ans:    make([]string, len(qs)),
		done:   make([]bool, len(qs)),
		review: prov == model.ProviderClaude && (len(qs) > 1 || (len(qs) == 1 && qs[0].Multi)),
	}
	for i := range qs {
		f.ticks[i] = map[int]bool{}
	}
	p.mu.Lock()
	p.screen = f.draw()
	p.wrote = f.onWrite
	p.mu.Unlock()
	return f
}

func (f *fakeStepTUI) onWrite() {
	in := string(f.p.input[f.read:])
	f.read = len(f.p.input)
	for in != "" && f.page >= 0 {
		switch {
		case strings.HasPrefix(in, "\x1b[200~"):
			end := strings.Index(in, "\x1b[201~")
			if end < 0 {
				f.buf += in[6:]
				in = ""
			} else {
				f.buf += in[6:end]
				in = in[end+6:]
			}
		case strings.HasPrefix(in, "\x1b[B"):
			f.cursor++
			in = in[3:]
		case strings.HasPrefix(in, "\x1b[A"):
			if f.cursor > 0 {
				f.cursor--
			}
			in = in[3:]
		case strings.HasPrefix(in, "\x1b[D"): // Left
			if f.page > 0 {
				f.page--
				f.cursor, f.notes, f.writeIn, f.buf = 0, false, false, ""
			}
			in = in[3:]
		case strings.HasPrefix(in, "\x1b[C"): // Right
			maxP := len(f.qs) - 1
			if f.review {
				maxP = len(f.qs)
			}
			if f.page < maxP {
				f.page++
				f.cursor, f.notes, f.writeIn, f.buf = 0, false, false, ""
			}
			in = in[3:]
		case in[0] == '\t':
			if f.prov == model.ProviderCodex {
				f.notes = !f.notes
			}
			in = in[1:]
		case in[0] == '\x1b':
			if f.writeIn {
				f.writeIn, f.buf = false, ""
			} else {
				f.page = -1
			}
			in = in[1:]
		case in[0] == ' ' && !f.notes && !f.writeIn && f.page < len(f.qs) && f.qs[f.page].Multi && f.cursor < len(f.qs[f.page].Options):
			f.ticks[f.page][f.cursor] = !f.ticks[f.page][f.cursor]
			f.done[f.page] = false
			for _, on := range f.ticks[f.page] {
				if on {
					f.done[f.page] = true
				}
			}
			in = in[1:]
		case in[0] == '\r':
			f.onEnter()
			in = in[1:]
		default:
			if in[0] >= 0x20 && in[0] < 0x7f {
				f.buf += string(in[0])
			}
			in = in[1:]
		}
	}
	f.p.screen = f.draw()
}

func (f *fakeStepTUI) onEnter() {
	if f.page == len(f.qs) { // Claude review
		if f.cursor == 0 {
			f.page = -1
		}
		return
	}
	q := f.qs[f.page]
	n := len(q.Options)
	switch f.prov {
	case model.ProviderClaude:
		if q.Multi {
			if f.cursor == n+1 { // Next / Submit
				var picked []string
				for i, o := range q.Options {
					if f.ticks[f.page][i] {
						picked = append(picked, o.Label)
					}
				}
				if f.buf != "" {
					picked = append(picked, f.buf)
				}
				f.ans[f.page] = strings.Join(picked, ", ")
				f.done[f.page] = len(picked) > 0
				f.advance()
			}
			return
		}
		if f.cursor < n {
			f.ans[f.page] = q.Options[f.cursor].Label
			f.done[f.page] = true
			f.advance()
		} else if f.cursor == n && f.buf != "" {
			f.ans[f.page] = f.buf
			f.done[f.page] = true
			f.advance()
		}
	case model.ProviderCodex:
		if f.cursor < n {
			f.ans[f.page] = q.Options[f.cursor].Label
			f.done[f.page] = true
			f.advance()
		} else if f.cursor == n && f.notes && f.buf != "" {
			f.ans[f.page] = f.buf
			f.done[f.page] = true
			f.advance()
		}
	case model.ProviderAntigravity:
		if f.writeIn {
			var picked []string
			if q.Multi {
				for i, o := range q.Options {
					if f.ticks[f.page][i] {
						picked = append(picked, o.Label)
					}
				}
			}
			if f.buf != "" {
				picked = append(picked, f.buf)
			}
			f.ans[f.page] = strings.Join(picked, ", ")
			f.done[f.page] = len(picked) > 0
			f.writeIn = false
			f.advance()
			return
		}
		if f.cursor == n { // Write-in...
			f.writeIn = true
			f.buf = ""
			return
		}
		if q.Multi {
			var picked []string
			for i, o := range q.Options {
				if f.ticks[f.page][i] {
					picked = append(picked, o.Label)
				}
			}
			f.ans[f.page] = strings.Join(picked, ", ")
			f.done[f.page] = len(picked) > 0
			f.advance()
			return
		}
		if f.cursor < n {
			f.ans[f.page] = q.Options[f.cursor].Label
			f.done[f.page] = true
			f.advance()
		}
	}
}

func (f *fakeStepTUI) advance() {
	f.page++
	f.cursor, f.notes, f.writeIn, f.buf = 0, false, false, ""
	if f.page >= len(f.qs) && !f.review {
		f.page = -1
	}
}

func (f *fakeStepTUI) draw() []string {
	if f.page < 0 {
		switch f.prov {
		case model.ProviderCodex:
			return []string{"", "› "}
		case model.ProviderAntigravity:
			return []string{"", "> ", "? for shortcuts   auto · Gemini 4 Neon · low"}
		default:
			return []string{"", "❯ "}
		}
	}
	switch f.prov {
	case model.ProviderClaude:
		return f.drawClaude()
	case model.ProviderCodex:
		return f.drawCodex()
	case model.ProviderAntigravity:
		return f.drawAgy()
	}
	return nil
}

func (f *fakeStepTUI) drawClaude() []string {
	sep := strings.Repeat("─", 80)
	mark := func(i int) string {
		if f.cursor == i {
			return "❯ "
		}
		return "  "
	}
	var tabs []string
	for i, q := range f.qs {
		box := "☐"
		if f.done[i] {
			box = "☒"
		}
		h := q.Header
		if h == "" {
			h = fmt.Sprintf("Q%d", i+1)
		}
		tabs = append(tabs, box+" "+h)
	}
	head := " " + strings.Join(tabs, "  ")
	if f.review {
		head = "←  " + strings.Join(tabs, "  ") + "  ✔ Submit  →"
	}
	if f.page == len(f.qs) {
		rows := []string{sep, head, "", "Review your answers", ""}
		for i, q := range f.qs {
			rows = append(rows, " ● "+q.Prompt, "   → "+f.ans[i])
		}
		rows = append(rows, "", mark(0)+"1. Submit answers", mark(1)+"2. Cancel")
		return rows
	}
	q := f.qs[f.page]
	rows := []string{sep, head, "", q.Prompt, ""}
	hasPreview := false
	for _, o := range q.Options {
		if o.Preview != "" {
			hasPreview = true
		}
	}
	for i, o := range q.Options {
		box := ""
		if q.Multi {
			if f.ticks[f.page][i] {
				box = "[✔] "
			} else {
				box = "[ ] "
			}
		}
		line := fmt.Sprintf("%s%d. %s%s", mark(i), i+1, box, o.Label)
		if hasPreview && i == 0 {
			line += "    ┌────────────┐"
		} else if hasPreview {
			line += "    │ preview    │"
		}
		rows = append(rows, line)
	}
	if !hasPreview {
		n := len(q.Options)
		box := ""
		if q.Multi {
			box = "[ ] "
		}
		other := "Type something."
		if f.buf != "" {
			other = f.buf
		}
		rows = append(rows, fmt.Sprintf("%s%d. %s%s", mark(n), n+1, box, other))
		if q.Multi {
			btn := "Next"
			if f.page == len(f.qs)-1 {
				btn = "Submit"
			}
			if f.cursor == n+1 {
				rows = append(rows, "❯    "+btn)
			} else {
				rows = append(rows, "     "+btn)
			}
		}
	}
	rows = append(rows, sep, "  Chat about this", "", "Enter to select · Tab/Arrow keys to navigate · Esc to cancel")
	return rows
}

func (f *fakeStepTUI) drawCodex() []string {
	q := f.qs[f.page]
	unans := 0
	for _, d := range f.done {
		if !d {
			unans++
		}
	}
	mark := func(i int) string {
		if f.cursor == i {
			return "  › "
		}
		return "    "
	}
	rows := []string{
		fmt.Sprintf("  Question %d/%d (%d unanswered)", f.page+1, len(f.qs), unans),
		"  " + q.Prompt,
		"",
	}
	for i, o := range q.Options {
		rows = append(rows, fmt.Sprintf("%s%d. %-18s  %s", mark(i), i+1, o.Label, o.Description))
	}
	rows = append(rows, fmt.Sprintf("%s%d. None of the above  Optionally, add details in notes (tab)", mark(len(q.Options)), len(q.Options)+1))
	if f.notes {
		txt := "Add notes"
		if f.buf != "" {
			txt = f.buf
		}
		rows = append(rows, "", "  › "+txt, "", "  tab or esc to clear notes | enter to submit all")
	} else {
		sub := "enter to submit answer"
		if f.page == len(f.qs)-1 {
			sub = "enter to submit all"
		}
		nav := ""
		if len(f.qs) > 1 {
			nav = " | ←/→ to navigate questions"
		}
		rows = append(rows, "", "  tab to add notes | "+sub+nav+" | esc to interrupt")
	}
	return rows
}

func (f *fakeStepTUI) drawAgy() []string {
	q := f.qs[f.page]
	var rows []string
	for _, sq := range f.qs {
		rows = append(rows, "? "+sq.Prompt)
	}
	rows = append(rows, "", "Question", strings.Repeat("─", 80), "", fmt.Sprintf("Question %d/%d: %s", f.page+1, len(f.qs), q.Prompt), "")
	mark := func(i int) string {
		if f.cursor == i {
			return "> "
		}
		return "  "
	}
	for i, o := range q.Options {
		box := ""
		if q.Multi {
			if f.ticks[f.page][i] {
				box = "[x] "
			} else {
				box = "[ ] "
			}
		}
		rows = append(rows, fmt.Sprintf("%s%d. %s%s", mark(i), i+1, box, o.Label))
	}
	rows = append(rows, fmt.Sprintf("%s%d. Write-in...", mark(len(q.Options)), len(q.Options)+1))
	if f.writeIn {
		rows = append(rows, "", "  Your answer:", "  "+f.buf, "", "  enter Submit · esc Back")
	} else {
		var hints []string
		hints = append(hints, "↑/↓ Navigate")
		if f.page > 0 {
			hints = append(hints, "← Back")
		}
		if f.page < len(f.qs)-1 {
			hints = append(hints, "→ Next")
		}
		if q.Multi {
			hints = append(hints, "space Toggle", "enter Submit")
		} else if f.page == len(f.qs)-1 && len(f.qs) > 1 {
			hints = append(hints, "enter Submit All")
		} else {
			hints = append(hints, "enter Select")
		}
		hints = append(hints, "esc Skip")
		rows = append(rows, "", "  "+strings.Join(hints, " · "))
	}
	rows = append(rows, "esc to cancel                                              auto · Gemini 4 Neon · low")
	return rows
}

func threeTestQuestions(multi bool) []session.Question {
	return []session.Question{
		{ID: "color", Header: "Color", Prompt: "Which color?", Options: []session.PromptOption{{Label: "Red", Description: "Warm"}, {Label: "Blue", Description: "Cool"}}},
		{ID: "toppings", Header: "Toppings", Prompt: "Which toppings?", Multi: multi, Options: []session.PromptOption{{Label: "Cheese", Description: "Dairy"}, {Label: "Olives", Description: "Fruit"}, {Label: "Onion", Description: "Veg"}}},
		{ID: "size", Header: "Size", Prompt: "Which size?", Options: []session.PromptOption{{Label: "Small", Description: "8 oz"}, {Label: "Large", Description: "16 oz"}}},
	}
}

// TestDriveQuestionSteps: step-by-step TUI driving across Claude Code, Codex
// and Antigravity, including multi-select, Back/Forward navigation, Other
// text, Claude's Review/Submit step, and preview options.
func TestDriveQuestionSteps(t *testing.T) {
	t.Run("claude-3q-steps", func(t *testing.T) {
		d, f, _ := remoteDaemon(t)
		qs := threeTestQuestions(true)
		tui := newFakeStepTUI(f.pane(0), model.ProviderClaude, qs)

		// Step 0: pick Blue
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderClaude, qs, 0, true, []string{"Blue"}); err != nil {
			t.Fatalf("step 0: %v", err)
		}
		if tui.page != 1 || tui.ans[0] != "Blue" {
			t.Fatalf("after step 0: page %d ans %q", tui.page, tui.ans)
		}
		// Step 1 (multi-select): pick Cheese and Onion -> Next
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderClaude, qs, 1, true, []string{"Cheese", "Onion"}); err != nil {
			t.Fatalf("step 1: %v", err)
		}
		if tui.page != 2 || tui.ans[1] != "Cheese, Onion" {
			t.Fatalf("after step 1: page %d ans %q", tui.page, tui.ans)
		}
		// Go back to step 1 (←), then forward to step 2 (→)
		if err := d.driveGoto("a", f.pane(0), model.ProviderClaude, qs, 1); err != nil {
			t.Fatalf("goto 1: %v", err)
		}
		if tui.page != 1 {
			t.Fatalf("expected page 1, got %d", tui.page)
		}
		if err := d.driveGoto("a", f.pane(0), model.ProviderClaude, qs, 2); err != nil {
			t.Fatalf("goto 2: %v", err)
		}
		// Step 2: Other "Medium" -> lands on Review
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderClaude, qs, 2, true, []string{"Medium"}); err != nil {
			t.Fatalf("step 2: %v", err)
		}
		if tui.page != 3 || tui.ans[2] != "Medium" {
			t.Fatalf("after step 2: page %d ans %q", tui.page, tui.ans)
		}
		// Submit Review
		if err := d.driveSubmitReview("a", f.pane(0), model.ProviderClaude); err != nil {
			t.Fatalf("submit review: %v", err)
		}
		if tui.page != -1 || !reflect.DeepEqual(tui.ans, []string{"Blue", "Cheese, Onion", "Medium"}) {
			t.Fatalf("final: page %d ans %q", tui.page, tui.ans)
		}
	})

	t.Run("claude-preview", func(t *testing.T) {
		d, f, _ := remoteDaemon(t)
		qs := []session.Question{{
			ID: "layout", Header: "Layout", Prompt: "Which layout?",
			Options: []session.PromptOption{
				{Label: "Split", Preview: "+---+---+\n| A | B |"},
				{Label: "Stack", Preview: "+-------+\n|   A   |"},
			},
		}}
		tui := newFakeStepTUI(f.pane(0), model.ProviderClaude, qs)
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderClaude, qs, 0, false, []string{"Stack"}); err != nil {
			t.Fatalf("preview: %v", err)
		}
		if tui.page != -1 || tui.ans[0] != "Stack" {
			t.Fatalf("preview final: page %d ans %q", tui.page, tui.ans)
		}
	})

	t.Run("codex-3q-steps", func(t *testing.T) {
		d, f, _ := remoteDaemon(t)
		d.mu.Lock()
		d.st.Panes[0].Provider = model.ProviderCodex
		d.mu.Unlock()
		qs := threeTestQuestions(false)
		tui := newFakeStepTUI(f.pane(0), model.ProviderCodex, qs)

		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderCodex, qs, 0, false, []string{"Blue"}); err != nil {
			t.Fatalf("step 0: %v", err)
		}
		if err := d.driveGoto("a", f.pane(0), model.ProviderCodex, qs, 0); err != nil {
			t.Fatalf("goto 0: %v", err)
		}
		if tui.page != 0 {
			t.Fatalf("expected page 0, got %d", tui.page)
		}
		if err := d.driveGoto("a", f.pane(0), model.ProviderCodex, qs, 1); err != nil {
			t.Fatalf("goto 1: %v", err)
		}
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderCodex, qs, 1, false, []string{"Olives"}); err != nil {
			t.Fatalf("step 1: %v", err)
		}
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderCodex, qs, 2, false, []string{"Medium"}); err != nil {
			t.Fatalf("step 2 other: %v", err)
		}
		if tui.page != -1 || !reflect.DeepEqual(tui.ans, []string{"Blue", "Olives", "Medium"}) {
			t.Fatalf("codex final: page %d ans %q", tui.page, tui.ans)
		}
	})

	t.Run("agy-3q-steps", func(t *testing.T) {
		d, f, _ := remoteDaemon(t)
		d.mu.Lock()
		d.st.Panes[0].Provider = model.ProviderAntigravity
		d.mu.Unlock()
		qs := threeTestQuestions(true)
		tui := newFakeStepTUI(f.pane(0), model.ProviderAntigravity, qs)

		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderAntigravity, qs, 0, false, []string{"Blue"}); err != nil {
			t.Fatalf("step 0: %v", err)
		}
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderAntigravity, qs, 1, false, []string{"Cheese", "Onion"}); err != nil {
			t.Fatalf("step 1 multi: %v", err)
		}
		if err := d.driveGoto("a", f.pane(0), model.ProviderAntigravity, qs, 1); err != nil {
			t.Fatalf("goto 1: %v", err)
		}
		if err := d.driveGoto("a", f.pane(0), model.ProviderAntigravity, qs, 2); err != nil {
			t.Fatalf("goto 2: %v", err)
		}
		if err := d.driveAnswerStep("a", f.pane(0), model.ProviderAntigravity, qs, 2, false, []string{"Medium"}); err != nil {
			t.Fatalf("step 2 write-in: %v", err)
		}
		if tui.page != -1 || !reflect.DeepEqual(tui.ans, []string{"Blue", "Cheese, Onion", "Medium"}) {
			t.Fatalf("agy final: page %d ans %q", tui.page, tui.ans)
		}
	})
}

// TestDriveQuestionMismatchStops: if the TUI is not on the question pitwall
// thinks it is, pitwall stops with a clear error note and types nothing.
func TestDriveQuestionMismatchStops(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	qs := threeTestQuestions(true)
	tui := newFakeStepTUI(f.pane(0), model.ProviderClaude, qs)
	// Advance TUI directly to page 1 ("Which toppings?") without telling pitwall.
	f.pane(0).mu.Lock()
	tui.page = 1
	f.pane(0).screen = tui.draw()
	before := len(f.pane(0).input)
	f.pane(0).mu.Unlock()

	err := d.driveAnswerStep("a", f.pane(0), model.ProviderClaude, qs, 0, true, []string{"Blue"})
	if err == nil || !strings.Contains(err.Error(), "Which toppings?") || !strings.Contains(err.Error(), "Which color?") {
		t.Fatalf("mismatch err = %v, want clear note naming both questions", err)
	}
	f.pane(0).mu.Lock()
	typed := string(f.pane(0).input[before:])
	f.pane(0).mu.Unlock()
	if typed != "" {
		t.Fatalf("mismatch typed keys %q", typed)
	}
}

// TestPaneChatSyncsWithTUI: answering directly in the TUI advances the
// snapshot's Step and Done flags, and withdrawing the question closes it.
func TestPaneChatSyncsWithTUI(t *testing.T) {
	for _, prov := range []model.Provider{model.ProviderCodex, model.ProviderAntigravity} {
		t.Run(string(prov), func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			d.mu.Lock()
			d.st.Panes[0].Provider = prov
			d.st.Activities = []model.Activity{{PaneID: "a", Provider: prov, State: model.StateAwaitingInput, UpdatedAt: time.Now()}}
			d.mu.Unlock()
			qs := threeTestQuestions(prov == model.ProviderAntigravity)
			tui := newFakeStepTUI(f.pane(0), prov, qs)

			pc, l, ok := d.paneChatOf("a")
			if !ok {
				t.Fatal("no pane chat")
			}
			pc.mu.Lock()
			pc.catchUp(l)
			in := pc.snap.PendingUserInputs
			pc.mu.Unlock()
			if len(in) != 1 || in[0].Step == nil || *in[0].Step != 0 || len(in[0].Questions) != 3 {
				t.Fatalf("initial sync: %+v", in)
			}

			// Simulate user answering step 0 directly in the TUI.
			f.pane(0).mu.Lock()
			tui.ans[0] = "Blue"
			tui.done[0] = true
			tui.page = 1
			f.pane(0).screen = tui.draw()
			f.pane(0).mu.Unlock()

			pc.mu.Lock()
			pc.catchUp(l)
			in = pc.snap.PendingUserInputs
			pc.mu.Unlock()
			if len(in) != 1 || in[0].Step == nil || *in[0].Step != 1 || !in[0].Done[0] {
				t.Fatalf("after TUI step 0: %+v", in)
			}

			// Simulate agent withdrawing the question (Esc / back to prompt).
			f.pane(0).mu.Lock()
			tui.page = -1
			f.pane(0).screen = tui.draw()
			f.pane(0).mu.Unlock()

			pc.mu.Lock()
			pc.catchUp(l)
			in = pc.snap.PendingUserInputs
			pc.mu.Unlock()
			if len(in) != 0 {
				t.Fatalf("withdrawn question still pending: %+v", in)
			}
		})
	}
}

// TestPhoneQuestionNavAndAgy: phoneQuestion handles Back (-3), Next (-4),
// Claude multi-select Next (-1), and Antigravity questions.
func TestPhoneQuestionNavAndAgy(t *testing.T) {
	for _, tc := range []struct {
		file   string
		prov   model.Provider
		option int
		text   string
		want   string
	}{
		{"claude-ask-3q-step2-multi.txt", model.ProviderClaude, -1, "", "\x1b[B\x1b[B\x1b[B\x1b[B\r"},
		{"claude-ask-3q-step2-multi.txt", model.ProviderClaude, -3, "", "\x1b[D"},
		{"claude-ask-3q-step2-multi.txt", model.ProviderClaude, -4, "", "\x1b[C"},
		{"codex-ask-3q-step2.txt", model.ProviderCodex, -3, "", "\x1b[D"},
		{"codex-ask-3q-step2.txt", model.ProviderCodex, -4, "", "\x1b[C"},
		{"agy-ask-3q-step1.txt", model.ProviderAntigravity, 1, "", "\x1b[B\r"},
		{"agy-ask-3q-step2-multi.txt", model.ProviderAntigravity, 1, "", "\x1b[B "},
		{"agy-ask-3q-step2-multi.txt", model.ProviderAntigravity, -1, "", "\r"},
		{"agy-ask-3q-step2-multi.txt", model.ProviderAntigravity, -3, "", "\x1b[D"},
		{"agy-ask-3q-step3.txt", model.ProviderAntigravity, -2, "Medium", "\x1b[B\x1b[B\rMedium\r"},
	} {
		t.Run(tc.file+"/"+fmt.Sprint(tc.option), func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			questionScreen(t, f, tc.file)
			d.mu.Lock()
			d.st.Panes[0].Provider = tc.prov
			d.mu.Unlock()
			q := agent.ReadQuestion(tc.prov, f.pane(0).Snapshot())
			if q == nil {
				t.Fatal("no question")
			}
			before := len(f.pane(0).got())
			if err := d.phoneQuestion("a", q.ID, tc.option, tc.text); err != nil {
				t.Fatal(err)
			}
			if got := gotAfter(t, f.pane(0), before, len(tc.want)); got != tc.want {
				t.Errorf("keys %q want %q", got, tc.want)
			}
		})
	}
}

// TestPaneAnswerClaudeLiveSteps: exercises paneAnswer end-to-end with
// Step="answer", "goto", and "submit" on a Claude Code 3-question flow,
// verifying live snapshot sync at every step.
func TestPaneAnswerClaudeLiveSteps(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderClaude
	d.st.Activities = []model.Activity{{PaneID: "a", Provider: model.ProviderClaude, State: model.StateAwaitingInput, UpdatedAt: time.Now()}}
	d.mu.Unlock()
	qs := threeTestQuestions(true)
	tui := newFakeStepTUI(f.pane(0), model.ProviderClaude, qs)

	pc, l, ok := d.paneChatOf("a")
	if !ok {
		t.Fatal("no pane chat")
	}
	// Seed a transcript-sourced PendingUserInput as Claude's AskUserQuestion tool does.
	pc.mu.Lock()
	pc.snap.PendingUserInputs = []session.InputRequest{{ID: "ask-1", Questions: qs}}
	pc.catchUp(l)
	in := pc.snap.PendingUserInputs
	pc.mu.Unlock()
	if len(in) != 1 || in[0].Step == nil || *in[0].Step != 0 || !in[0].Review {
		t.Fatalf("initial claude sync: %+v", in)
	}

	// Step 0: pick Blue
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "answer", Target: 0, Answers: map[string][]string{qs[0].ID: {"Blue"}}}); err != nil {
		t.Fatalf("paneAnswer step 0: %v", err)
	}
	pc.mu.Lock()
	pc.catchUp(l)
	in = pc.snap.PendingUserInputs
	pc.mu.Unlock()
	if len(in) != 1 || in[0].Step == nil || *in[0].Step != 1 || !in[0].Done[0] {
		t.Fatalf("after step 0: %+v", in)
	}

	// Step 1 (multi): pick Cheese + Onion
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "answer", Target: 1, Answers: map[string][]string{qs[1].ID: {"Cheese", "Onion"}}}); err != nil {
		t.Fatalf("paneAnswer step 1: %v", err)
	}
	// Go back to step 0 via goto, then forward to step 2
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "goto", Target: 0}); err != nil {
		t.Fatalf("paneAnswer goto 0: %v", err)
	}
	if tui.page != 0 {
		t.Fatalf("after goto 0: page %d", tui.page)
	}
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "goto", Target: 2}); err != nil {
		t.Fatalf("paneAnswer goto 2: %v", err)
	}
	// Step 2: Other "Medium" -> lands on Review step (3)
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "answer", Target: 2, Answers: map[string][]string{qs[2].ID: {"Medium"}}}); err != nil {
		t.Fatalf("paneAnswer step 2: %v", err)
	}
	pc.mu.Lock()
	pc.catchUp(l)
	in = pc.snap.PendingUserInputs
	pc.mu.Unlock()
	if len(in) != 1 || in[0].Step == nil || *in[0].Step != 3 {
		t.Fatalf("at review: %+v", in)
	}
	// Final submit
	if err := d.paneAnswer("a", l, proto.ChatAnswer{Chat: "a", Request: "ask-1", Step: "submit", Target: 3}); err != nil {
		t.Fatalf("paneAnswer submit: %v", err)
	}
	if tui.page != -1 || !reflect.DeepEqual(tui.ans, []string{"Blue", "Cheese, Onion", "Medium"}) {
		t.Fatalf("final claude: page %d ans %q", tui.page, tui.ans)
	}
	_ = context.Background
}
