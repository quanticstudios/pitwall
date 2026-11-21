package daemon

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// fakeAsk is Claude Code's AskUserQuestion pages on a fake pane: it takes
// the keys pitwall types and draws the next page 30ms later, as a busy
// TUI does, so a key sent before the redraw lands on a stale page.
type fakeAsk struct {
	p       *fakePane
	qs      []session.Question
	page    int // len(qs) for the review page, -1 once sent
	cursor  int
	ticks   map[int]bool
	read    int      // input bytes taken
	got     []string // per question, the labels picked
	stale   int      // redraws pending
	reviews int      // review pages shown
}

func newFakeAsk(p *fakePane, qs []session.Question) *fakeAsk {
	a := &fakeAsk{p: p, qs: qs, ticks: map[int]bool{}, got: make([]string, len(qs))}
	p.mu.Lock()
	p.screen = a.draw()
	p.wrote = a.keys
	p.mu.Unlock()
	return a
}

// keys runs under p.mu after each write.
func (a *fakeAsk) keys() {
	in := string(a.p.input[a.read:])
	a.read = len(a.p.input)
	for in != "" {
		switch {
		case strings.HasPrefix(in, "\x1b[B"):
			a.cursor++
			in = in[3:]
		case strings.HasPrefix(in, "\x1b[A"):
			a.cursor--
			in = in[3:]
		case in[0] == ' ':
			a.ticks[a.cursor] = !a.ticks[a.cursor]
			in = in[1:]
		case in[0] == '\r':
			a.enter()
			in = in[1:]
		default:
			in = in[1:]
		}
	}
	a.stale++
	time.AfterFunc(30*time.Millisecond, func() {
		a.p.mu.Lock()
		defer a.p.mu.Unlock()
		if a.stale--; a.stale == 0 {
			a.p.screen = a.draw()
		}
	})
}

func (a *fakeAsk) enter() {
	if a.page == len(a.qs) {
		if a.cursor == 0 {
			a.page = -1
		}
		return
	}
	q := a.qs[a.page]
	if q.Multi {
		if a.cursor != len(q.Options)+1 {
			return // Enter on an option of a multi-select page does not submit it
		}
		var picked []string
		for i, o := range q.Options {
			if a.ticks[i] {
				picked = append(picked, o.Label)
			}
		}
		a.got[a.page] = strings.Join(picked, ", ")
	} else if a.cursor < len(q.Options) {
		a.got[a.page] = q.Options[a.cursor].Label
	} else {
		a.page = -2 // Chat about this, or Other without text: the answer is lost
		return
	}
	a.page, a.cursor, a.ticks = a.page+1, 0, map[int]bool{}
	if a.page == len(a.qs) && len(a.qs) == 1 && !q.Multi {
		a.page = -1 // one single-select question needs no review
	}
}

func (a *fakeAsk) draw() []string {
	sep := strings.Repeat("─", 80)
	mark := func(i int) string {
		if a.cursor == i {
			return "❯ "
		}
		return "  "
	}
	switch {
	case a.page < 0:
		return []string{"", "❯ "}
	case a.page == len(a.qs):
		a.reviews++
		return []string{sep, "←  ☒ Color  ✔ Submit  →", "", "Review your answers", "", " ● Which?", "   → " + strings.Join(a.got, ", "), "",
			mark(0) + "1. Submit answers", mark(1) + "2. Cancel"} // as 2.1.330 draws it in 24 rows
	}
	q := a.qs[a.page]
	rows := []string{sep, "←  ☐ Color  ☐ Size  ✔ Submit  →", "", q.Prompt, ""}
	box := func(i int) string {
		switch {
		case !q.Multi:
			return ""
		case a.ticks[i]:
			return "[✔] "
		}
		return "[ ] "
	}
	n := len(q.Options)
	for i, o := range q.Options {
		rows = append(rows, fmt.Sprintf("%s%d. %s%s", mark(i), i+1, box(i), o.Label))
	}
	rows = append(rows, fmt.Sprintf("%s%d. %sType something", mark(n), n+1, box(n)))
	if q.Multi {
		rows = append(rows, map[bool]string{true: "❯    Submit", false: "     Submit"}[a.cursor == n+1])
	}
	return append(rows, sep, fmt.Sprintf("  %d. Chat about this", n+2+map[bool]int{true: 1}[q.Multi]), "", "Enter to select · ↑/↓ to navigate · Esc to cancel")
}

// TestAnswerPages: answers reach Claude Code's question pages as typed,
// though each page draws a beat after its key: several picks on a
// multi-select page, two questions in a row, and the review page's Submit
// answers after either; a single question sends at once.
func TestAnswerPages(t *testing.T) {
	q := func(id string, multi bool, opts ...string) session.Question {
		sq := session.Question{ID: id, Prompt: "Which " + id + "?", Multi: multi}
		for _, o := range opts {
			sq.Options = append(sq.Options, session.PromptOption{Label: o})
		}
		return sq
	}
	for _, tc := range []struct {
		name    string
		qs      []session.Question
		answers map[string][]string
		want    []string
	}{
		{"multi", []session.Question{q("fruit", true, "Apple", "Banana", "Cherry")}, map[string][]string{"fruit": {"Apple", "Cherry"}}, []string{"Apple, Cherry"}},
		{"two", []session.Question{q("color", false, "Red", "Blue"), q("size", false, "Small", "Large")}, map[string][]string{"color": {"Blue"}, "size": {"Small"}}, []string{"Blue", "Small"}},
		{"single", []session.Question{q("color", false, "Red", "Blue")}, map[string][]string{"color": {"Blue"}}, []string{"Blue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			a := newFakeAsk(f.pane(0), tc.qs)
			start := time.Now()
			if err := d.answerPages("a", f.pane(0), tc.qs, tc.answers); err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			time.Sleep(60 * time.Millisecond) // the last redraw
			f.pane(0).mu.Lock()
			defer f.pane(0).mu.Unlock()
			if a.page != -1 || !reflect.DeepEqual(a.got, tc.want) {
				t.Fatalf("page %d, answers %q, keys %q", a.page, a.got, f.pane(0).input)
			}
			if tc.name == "single" && took > time.Second {
				t.Fatalf("a single question took %v", took)
			}
		})
	}
}
