package panel

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// resolvedQ is one question of an answered card, for its open line.
type resolvedQ struct {
	header, prompt string
	options        []session.PromptOption
	picked         []string // the labels picked, or the typed answer
}

// askItem is whether item it is a question call: Claude's AskUserQuestion.
func askItem(it *session.Item) bool {
	return it.Kind == session.KindToolCall && it.Tool != nil && it.Tool.ToolName == "AskUserQuestion"
}

// askAnswered is an answered question call's line: its questions with
// what was picked, read from the call's input and from the output Claude
// Code writes, `"question"="answer"` per question, whoever answered it.
// ok is false while the call waits.
func askAnswered(it *session.Item) (r resolved, ok bool) {
	tc := it.Tool
	if tc == nil || tc.Output == "" {
		return r, false
	}
	var in struct {
		Questions []struct {
			Question, Header string
			MultiSelect      bool
			Options          []struct{ Label, Description, Preview string }
		}
	}
	json.Unmarshal(tc.Input, &in)
	r = resolved{id: it.ID, line: "Answered", prose: true, question: true}
	var all []string
	for i, q := range in.Questions {
		rq := resolvedQ{header: q.Header, prompt: q.Question}
		for _, o := range q.Options {
			rq.options = append(rq.options, session.PromptOption{Label: o.Label, Description: o.Description, Preview: o.Preview})
		}
		next := ""
		if i+1 < len(in.Questions) {
			next = in.Questions[i+1].Question
		}
		if a := answerOf(tc.Output, q.Question, next); a != "" {
			rq.picked = pickedOf(a, rq.options, q.MultiSelect)
			all = append(all, a)
		}
		r.qs = append(r.qs, rq)
	}
	r.what = strings.Join(all, ", ")
	if len(all) == 0 { // declined, or a reply this cannot read
		r.line, r.denied, r.what = "Not answered", true, firstLine(tc.Output)
	}
	return r, true
}

// answerOf is question q's answer in out, `"q"="answer"`, which runs to
// the next question's `, "next"=`, else to the last quote.
func answerOf(out, q, next string) string {
	mark := `"` + q + `"="`
	i := strings.Index(out, mark)
	if i < 0 {
		return ""
	}
	rest := out[i+len(mark):]
	end := -1
	if next != "" {
		end = strings.Index(rest, `", "`+next+`"="`)
	}
	if end < 0 {
		end = strings.LastIndex(rest, `"`)
	}
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// pickedOf splits answer a into the options it names: a multiSelect
// answer is the labels joined with ", ", any text left a typed one.
func pickedOf(a string, opts []session.PromptOption, multi bool) []string {
	if !multi {
		return []string{a}
	}
	var out []string
	for rest := a; rest != ""; {
		hit := ""
		for _, o := range opts {
			if (rest == o.Label || strings.HasPrefix(rest, o.Label+", ")) && len(o.Label) > len(hit) {
				hit = o.Label
			}
		}
		if hit == "" {
			return append(out, rest) // typed text, which may hold commas
		}
		out = append(out, hit)
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, hit), ", ")
	}
	return out
}

// askLine is the Answered line of the answered question call at item i,
// kept while its output stays the same so its open state holds.
func (t *Transcript) askLine(s *session.Snapshot, i int) *resolved {
	it := &s.Content[i]
	if t.asked == nil {
		t.asked = map[string]*askCache{}
	}
	c := t.asked[it.ID]
	if c == nil || c.out != it.Tool.Output {
		r, _ := askAnswered(it)
		r.when = parseTime(it.UpdatedAt)
		c = &askCache{out: it.Tool.Output, r: r}
		t.asked[it.ID] = c
	}
	return &c.r
}

// askCache is an answered question call's line, by the output it read.
type askCache struct {
	out string
	r   resolved
}

// cuts are the items before which the lines answered in this window go,
// so the work after an answer draws below it. A question's line goes once
// the transcript has the call it answered with its answer: that call's
// own Answered line, where it was asked, takes its place.
func (t *Transcript) cuts(s *session.Snapshot) []int {
	var out []int
	for k := range t.done {
		r := &t.done[k]
		r.gone = r.question && len(r.qs) > 0 && slices.ContainsFunc(s.Content[:min(r.at, len(s.Content))], func(it session.Item) bool {
			if !askItem(&it) || it.Tool.Output == "" {
				return false
			}
			a, _ := askAnswered(&it)
			return len(a.qs) > 0 && a.qs[0].prompt == r.qs[0].prompt
		})
		if !r.gone && r.at > 0 && r.at < len(s.Content) {
			out = append(out, r.at)
		}
	}
	return out
}
