package mobile

import (
	"context"
	"image"
	"reflect"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
)

// inputAPI records the answers the phone sends.
type inputAPI struct {
	fakeAPI
	inputs    []map[string][]string
	questions []string
	err       error
}

func (f *inputAPI) Input(ctx context.Context, chat, request string, answers map[string][]string) error {
	f.inputs = append(f.inputs, answers)
	return f.err
}

func (f *inputAPI) Question(ctx context.Context, pane, id string, option int, text string) error {
	f.questions = append(f.questions, text)
	return f.err
}

func askTab(rs ...remote.Request) remote.Tab {
	return remote.Tab{Item: remote.Item{Pane: "c", Agent: "claude", State: "awaiting-input", Requests: rs}, Send: true}
}

func askReq(id string, qs ...session.Question) remote.Request {
	return remote.Request{ID: id, Kind: "input", Questions: qs}
}

func colorQ(id string, multi bool) session.Question {
	return session.Question{ID: id, Prompt: "Which?", Multi: multi, Options: []session.PromptOption{{Label: "Red"}, {Label: "Blue"}}}
}

// TestPhoneQuestion: a tap that leaves no question open sends the card,
// which shows its answers at once; multiSelect taps wait for Send; text
// sent from the box answers the open question; a draft from before the
// question, a slash command, an image, or an approval waiting too keep
// the box's usual send; a failed answer brings the questions back.
func TestPhoneQuestion(t *testing.T) {
	api := &inputAPI{}
	var ops op.Ops
	gtx := C{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 900)}}
	envOf := func(tab remote.Tab) chatEnv {
		return chatEnv{Tab: tab, Open: true, Online: true, Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
	}
	draw := func(c *chatView, env chatEnv) { ops.Reset(); c.askCard(gtx, Theme(), env) }

	cv := newChat("c")
	c := &cv
	env := envOf(askTab(askReq("i1", colorQ("q", false))))
	draw(c, env)
	if placeholder(env.Tab) != "Pick an answer above, or type your own" {
		t.Fatalf("placeholder %q", placeholder(env.Tab))
	}
	c.card.reqs[0].opts[0][1].Click()
	draw(c, env)
	c.card.reqs[0].opts[0][0].Click() // a second tap after the send
	draw(c, env)
	if want := []map[string][]string{{"q": {"Blue"}}}; !reflect.DeepEqual(api.inputs, want) || c.card.reqs[0].sent != "Blue" {
		t.Fatalf("one tap: %v, shown %q", api.inputs, c.card.reqs[0].sent)
	}

	// Two questions, the second multiSelect: a tap, then the box.
	api.inputs = nil
	cv = newChat("c")
	env = envOf(askTab(askReq("i2", colorQ("a", false), colorQ("b", true))))
	draw(c, env)
	c.card.reqs[0].opts[0][0].Click()
	draw(c, env)
	c.card.reqs[0].opts[1][1].Click()
	draw(c, env)
	if len(api.inputs) != 0 {
		t.Fatalf("sent with a multiSelect question open: %v", api.inputs)
	}
	if !c.answerFromBox(env, "Green ", false) {
		t.Fatal("the box's text was not taken")
	}
	if want := []map[string][]string{{"a": {"Red"}, "b": {"Blue", "Green"}}}; !reflect.DeepEqual(api.inputs, want) {
		t.Fatalf("box answer: %v", api.inputs)
	}

	// The box fills the first open question and waits for the rest.
	api.inputs = nil
	cv = newChat("c")
	env = envOf(askTab(askReq("i3", colorQ("a", false), colorQ("b", false))))
	draw(c, env)
	if !c.answerFromBox(env, "Green", false) || len(api.inputs) != 0 || c.card.reqs[0].texts[0].Text() != "Green" {
		t.Fatalf("first open question: %v", api.inputs)
	}
	c.card.reqs[0].opts[1][0].Click()
	draw(c, env)
	if want := []map[string][]string{{"a": {"Green"}, "b": {"Red"}}}; !reflect.DeepEqual(api.inputs, want) {
		t.Fatalf("after the tap: %v", api.inputs)
	}

	// What the box keeps for its usual send.
	cv = newChat("c")
	c.cmp.ed.SetText("a message from before")
	env = envOf(askTab(askReq("i4", colorQ("q", false))))
	draw(c, env)
	for _, tc := range []struct {
		text  string
		image bool
	}{{"a message from before", false}, {"/compact", false}, {"look at this", true}, {"  ", false}} {
		if c.answerFromBox(env, tc.text, tc.image) {
			t.Errorf("%q (image %v) answered", tc.text, tc.image)
		}
	}
	both := envOf(askTab(remote.Request{ID: "a1", Kind: "approval"}, askReq("i5", colorQ("q", false))))
	draw(c, both)
	if c.answerFromBox(both, "Red", false) || answerable(both.Tab) {
		t.Fatal("the box answered with an approval waiting too")
	}

	// A failed answer: the questions come back with the error.
	api.inputs, api.err = nil, context.DeadlineExceeded
	cv = newChat("c")
	env = envOf(askTab(askReq("i6", colorQ("q", false))))
	draw(c, env)
	c.card.reqs[0].opts[0][0].Click()
	draw(c, env)
	if len(api.inputs) != 1 || c.card.reqs[0].sent != "" || !c.card.bad {
		t.Fatalf("failure: %v, shown %q, note %q", api.inputs, c.card.reqs[0].sent, c.card.note)
	}
	api.err = nil

	// A terminal question's typed row takes the box's text.
	cv = newChat("c")
	tui := remote.Tab{Item: remote.Item{Pane: "p", Agent: "claude", State: "awaiting-input", Question: &agent.Question{ID: "t1", Prompt: "Which?", Other: true}}, Send: true}
	draw(c, envOf(tui))
	if !c.answerFromBox(envOf(tui), "Green", false) || len(api.questions) != 1 || api.questions[0] != "Green" {
		t.Fatalf("terminal question: %v", api.questions)
	}
}

type navAPI struct {
	fakeAPI
	opts []int
}

func (f *navAPI) Question(_ context.Context, _, _ string, option int, _ string) error {
	f.opts = append(f.opts, option)
	return nil
}

// TestPhoneQuestionSteps: a 3-question card shows one step at a time
// with clickable step chips, Back/Next navigation keeping answers,
// live TUI step syncing, and terminalQuestion Back (-3) / Next (-4).
func TestPhoneQuestionSteps(t *testing.T) {
	api := &inputAPI{}
	var ops op.Ops
	gtx := C{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 900)}}
	envOf := func(tab remote.Tab) chatEnv {
		return chatEnv{Tab: tab, Open: true, Online: true, Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
	}
	draw := func(c *chatView, env chatEnv) { ops.Reset(); c.askCard(gtx, Theme(), env) }

	q1 := session.Question{ID: "c", Header: "Color", Prompt: "Pick color", Options: []session.PromptOption{{Label: "Red", Preview: "color: #f00"}, {Label: "Blue"}}}
	q2 := session.Question{ID: "t", Header: "Toppings", Prompt: "Pick toppings", Multi: true, Options: []session.PromptOption{{Label: "Cheese"}, {Label: "Mushrooms"}}}
	q3 := session.Question{ID: "s", Header: "Size", Prompt: "Pick size", Options: []session.PromptOption{{Label: "Small"}, {Label: "Large"}}}

	cv := newChat("c")
	c := &cv
	env := envOf(askTab(askReq("i3q", q1, q2, q3)))
	draw(c, env)
	rs := c.card.reqs[0]
	if rs.step != 0 {
		t.Fatalf("initial step %d, want 0", rs.step)
	}
	// Step 1 single-select advances to step 2.
	rs.opts[0][0].Click()
	draw(c, env)
	if rs.step != 1 || len(api.inputs) != 0 {
		t.Fatalf("after step 1 pick: step %d, inputs %v", rs.step, api.inputs)
	}
	// Step 2 multi-select toggles Cheese and advances on Next.
	rs.opts[1][0].Click()
	draw(c, env)
	if rs.step != 1 {
		t.Fatalf("multi tap moved step to %d", rs.step)
	}
	rs.next.Click()
	draw(c, env)
	if rs.step != 2 {
		t.Fatalf("after Next: step %d, want 2", rs.step)
	}
	// Back returns to step 2 with Cheese kept; chip 0 returns to step 1 with Red kept.
	rs.back.Click()
	draw(c, env)
	if rs.step != 1 || !reflect.DeepEqual(rs.picked[1], []string{"Cheese"}) {
		t.Fatalf("after Back: step %d, picked %v", rs.step, rs.picked[1])
	}
	rs.chips[0].Click()
	draw(c, env)
	if rs.step != 0 || !reflect.DeepEqual(rs.picked[0], []string{"Red"}) {
		t.Fatalf("after chip 0: step %d, picked %v", rs.step, rs.picked[0])
	}
	// Jump back to step 3 via chip and pick Large -> sends all 3 answers.
	rs.chips[2].Click()
	draw(c, env)
	rs.opts[2][1].Click()
	draw(c, env)
	want := []map[string][]string{{"c": {"Red"}, "t": {"Cheese"}, "s": {"Large"}}}
	if !reflect.DeepEqual(api.inputs, want) {
		t.Fatalf("inputs %v, want %v", api.inputs, want)
	}

	// Live TUI step sync moves the phone card when the TUI advances.
	step0, step1 := 0, 1
	req := askReq("sync1", q1, q2)
	req.Step = &step0
	cv = newChat("c")
	c = &cv
	draw(c, envOf(askTab(req)))
	if c.card.reqs[0].step != 0 {
		t.Fatalf("sync initial step %d", c.card.reqs[0].step)
	}
	req.Step = &step1
	req.Answered = map[string][]string{"c": {"Blue"}}
	draw(c, envOf(askTab(req)))
	if c.card.reqs[0].step != 1 || !reflect.DeepEqual(c.card.reqs[0].picked[0], []string{"Blue"}) {
		t.Fatalf("after TUI advance: step %d, picked %v", c.card.reqs[0].step, c.card.reqs[0].picked[0])
	}

	// terminalQuestion Back (-3) and Next (-4) buttons.
	nav := &navAPI{}
	cv = newChat("p")
	c = &cv
	tui := remote.Tab{Item: remote.Item{Pane: "p", Agent: "claude", State: "awaiting-input", Question: &agent.Question{
		ID: "tq1", Index: 1, Total: 3, Prompt: "Pick toppings",
		Tabs:    []agent.QuestionTab{{Label: "Color", Answered: true}, {Label: "Toppings"}, {Label: "Size"}},
		Options: []agent.QuestionOption{{Label: "Cheese"}},
		CanBack: true, CanNext: true,
	}}, Send: true}
	navEnv := chatEnv{Tab: tui, Open: true, Online: true, Do: func(call func(context.Context, chatAPI) error, done func(error)) {
		done(call(context.Background(), nav))
	}}
	draw(c, navEnv)
	c.card.qBack.Click()
	draw(c, navEnv)
	c.card.qNext.Click()
	draw(c, navEnv)
	if !reflect.DeepEqual(nav.opts, []int{-3, -4}) {
		t.Fatalf("terminal nav opts %v, want [-3 -4]", nav.opts)
	}
}
