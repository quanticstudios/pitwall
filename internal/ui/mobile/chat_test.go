package mobile

import (
	"context"
	"errors"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/x/markdown"
	"gioui.org/x/richtext"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestModelName(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":     "Opus 5.5",
		"claude-opus-5-5[1m]": "Opus 5.5 1M",
		"Claude-Sonnet-5-5":   "Sonnet 5.5",
		"opus[1m]":            "Opus 5.5 1M",
		"default":             "Default",
		"gpt-6.1-sol":         "GPT-6.1 Sol",
		"claude-mystery-9":    "mystery-9", // not in the table: the id, less claude-
		"":                    "",
	} {
		if got := modelName(id); got != want {
			t.Errorf("modelName(%q) = %q, want %q", id, got, want)
		}
	}
	for _, c := range []struct{ pick, current, want string }{
		{"default", "claude-opus-5-5", "what Claude Code starts with"},
		{"opus", "claude-opus-5-5", "now"},
		{"opus[1m]", "claude-opus-5-5", "1M context"},
		{"opus[1m]", "claude-opus-5-5[1m]", "now"},
		{"sonnet", "claude-opus-5-5", ""},
		{"sonnet", "", ""},
	} {
		if got := modelNote(c.pick, c.current); got != c.want {
			t.Errorf("modelNote(%q, %q) = %q, want %q", c.pick, c.current, got, c.want)
		}
	}
	if effortLevel("low") != 1 || effortLevel("max") != 5 || effortLevel("") != 0 {
		t.Error("effortLevel: want low 1, max 5, unknown 0")
	}
}

func TestSplitFences(t *testing.T) {
	got := splitFences("Found it.\n\n```go\nfunc f() {\n}\n```\nDone.\n```\nstill streaming")
	want := []mdPart{{text: "Found it."}, {code: true, text: "func f() {\n}"}, {text: "Done."}, {code: true, text: "still streaming"}}
	if len(got) != len(want) {
		t.Fatalf("parts = %+v", got)
	}
	for i := range want {
		if got[i].code != want[i].code || got[i].text != want[i].text {
			t.Errorf("part %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if p := splitFences("just text"); len(p) != 1 || p[0].code {
		t.Errorf("plain text = %+v", p)
	}
}

func TestSendState(t *testing.T) {
	for _, c := range []struct {
		text                  string
		sending, online, busy bool
		want                  sendState
	}{
		{"", false, true, false, sendIdle},
		{"  \n", false, true, true, sendIdle},
		{"hi", false, true, false, sendReady},
		{"hi", false, true, true, sendQueue},
		{"hi", true, true, true, sendSending},
		{"hi", true, false, false, sendOff}, // offline wins: nothing goes out
	} {
		if got := sendStateOf(c.text, c.sending, c.online, c.busy); got != c.want {
			t.Errorf("sendStateOf(%q, %v, %v, %v) = %d, want %d", c.text, c.sending, c.online, c.busy, got, c.want)
		}
	}
	working := remote.Tab{Item: remote.Item{Agent: "claude", State: "working"}}
	if placeholder(working) != "Queue a message. Enter adds a line." {
		t.Errorf("working placeholder = %q", placeholder(working))
	}
	done := remote.Tab{Item: remote.Item{Agent: "codex", State: "completed"}}
	if placeholder(done) != "Message Codex. Enter adds a line." {
		t.Errorf("idle placeholder = %q", placeholder(done))
	}
	if !busy(remote.Tab{Item: remote.Item{State: "pending-approval"}}) || busy(done) {
		t.Error("busy: an approval queues, a finished turn does not")
	}
	if idle(remote.Tab{Item: remote.Item{State: "awaiting-input"}}) || !idle(done) {
		t.Error("idle: a question is not idle, a finished turn is")
	}
}

func TestQueueLine(t *testing.T) {
	ps := []*phone.Pending{
		{Text: "sent, not queued", Sent: true},
		{Text: "Run the  benchmarks\nfirst", Sent: true, Queued: true},
		{Text: "second", Sent: true, Queued: true},
	}
	b, r, bad := queueLine(remote.Tab{Queued: 2}, ps, "")
	if b != "2 queued" || r != "Run the benchmarks first" || bad {
		t.Errorf("queued = %q %q %v", b, r, bad)
	}
	// Messages queued from the desktop count, though the phone has no text.
	if b, r, _ := queueLine(remote.Tab{Queued: 3}, nil, ""); b != "3 queued" || r != "" {
		t.Errorf("desktop queue = %q %q", b, r)
	}
	if b, r, bad := queueLine(remote.Tab{Queued: 1}, ps, "the agent exited"); b != "Not sent:" || r != "the agent exited" || !bad {
		t.Errorf("not sent = %q %q %v", b, r, bad)
	}
	if b, _, _ := queueLine(remote.Tab{}, ps[:1], ""); b != "" {
		t.Errorf("nothing queued shows %q", b)
	}
}

func TestNotSentComesBack(t *testing.T) {
	now := time.Now()
	c := &phone.Chat{Pending: []*phone.Pending{{Text: "ok", Sent: true, At: now}, {Text: "open a PR", Sent: true, ID: 4, At: now}}}
	c.Update(remote.Tab{Dropped: []uint64{4}}, now)
	why, text := takeNotSent(c, "")
	if why != "the agent exited" || text != "open a PR" || len(c.Pending) != 1 {
		t.Fatalf("empty box: %q %q, %d pending", why, text, len(c.Pending))
	}
	// A draft in the box stays: the line still says why.
	c.Pending = append(c.Pending, &phone.Pending{Text: "lost", Err: "refused"})
	if why, text := takeNotSent(c, "my draft"); why != "refused" || text != "" {
		t.Errorf("with a draft: %q %q", why, text)
	}
	if why, _ := takeNotSent(c, ""); why != "" {
		t.Errorf("nothing left, got %q", why)
	}
}

// fakeAPI records the chat's calls.
type fakeAPI struct {
	chatAPI
	commands []string
	err      error
}

func (f *fakeAPI) Command(ctx context.Context, pane, what, value string) error {
	f.commands = append(f.commands, pane+" /"+what+" "+value)
	return f.err
}

func TestPickWaitsForIdle(t *testing.T) {
	api := &fakeAPI{}
	c := newChat("p1")
	c.data.Model = "claude-opus-5-5"
	var later []func()
	env := chatEnv{Online: true, Tab: remote.Tab{Item: remote.Item{Pane: "p1", State: "working"}},
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			err := call(context.Background(), api)
			later = append(later, func() { done(err) })
		}}
	c.cmp.pick = &pick{"model", "sonnet"}
	c.firePick(env)
	if len(api.commands) != 0 {
		t.Fatalf("fired mid-turn: %v", api.commands)
	}
	if got := c.cmp.modelShown(c.data.Model); got != "Opus 5.5" {
		t.Errorf("before the pick goes in, the chip says %q", got)
	}
	env.Tab.State = "completed"
	c.firePick(env)
	c.firePick(env) // one at a time
	if !slices.Equal(api.commands, []string{"p1 /model sonnet"}) {
		t.Fatalf("commands = %v", api.commands)
	}
	later[0]()
	if c.cmp.pick != nil || c.cmp.picking || c.cmp.modelShown(c.data.Model) != "Sonnet 5.5" {
		t.Errorf("after: pick %v, picking %v, chip %q", c.cmp.pick, c.cmp.picking, c.cmp.modelShown(c.data.Model))
	}
	// The next turn reports its own model, which the chip then follows.
	if got := c.cmp.modelShown("claude-haiku-5-5"); got != "Haiku 5.5" {
		t.Errorf("after a turn, the chip says %q", got)
	}

	// A turn that began before the pick went in leaves it waiting.
	api.err = errors.New("Claude Code is working; try again when it waits at its input box")
	c.cmp.pick = &pick{"model", "haiku"}
	c.firePick(env)
	later[len(later)-1]()
	if c.cmp.pick == nil || c.cmp.picking || c.cmp.note != "" {
		t.Errorf("busy refusal: pick %v, picking %v, note %q", c.cmp.pick, c.cmp.picking, c.cmp.note)
	}

	api.err = errors.New("that pane shows a form; answer it in its tab")
	c.cmp.pick = &pick{"effort", "max"}
	c.firePick(env)
	later[len(later)-1]()
	if c.cmp.effort != "" || c.cmp.note != "That pane shows a form; answer it in its tab" {
		t.Errorf("failed pick: effort %q, note %q", c.cmp.effort, c.cmp.note)
	}
}

func TestPageInKeepsPlace(t *testing.T) {
	c := newChat("p1")
	c.reset(phone.Entries{Entries: []flow.Entry{{Seq: 10}, {Seq: 11}}, More: true})
	c.list.Position.First, c.list.Position.Offset = 1, 30
	c.pageIn([]flow.Entry{{Seq: 7}, {Seq: 8}, {Seq: 10}}, false)
	if c.list.Position.First != 3 || c.list.Position.Offset != 30 || c.data.More {
		t.Errorf("position %+v, more %v", c.list.Position, c.data.More)
	}
	if got := c.data.First(); got != 7 {
		t.Errorf("first seq %d", got)
	}
}

func TestOfflineText(t *testing.T) {
	now := time.Unix(100, 0)
	if got := offlineText(now.Add(3200*time.Millisecond), now); got != "Offline, reconnecting in 4 s" {
		t.Errorf("counting down: %q", got)
	}
	if got := offlineText(time.Time{}, now); got != "Offline, reconnecting" {
		t.Errorf("no retry time: %q", got)
	}
}

// Inline code sized by monoScale has the reply text's x-height, and the
// patched styledtext draws it on the text's baseline. It draws large, at
// 120px per em, where upstream's top alignment is 2 to 3px off.
func TestInlineCode(t *testing.T) {
	th := Theme()
	k := monoScale(th)
	if k < 0.8 || k > 1.2 {
		t.Fatalf("monoScale = %v", k)
	}
	if ui, mono := xHeight(th, th.UIFont), float32(xHeight(th, th.MonoFont))*k; mono < float32(ui)-1 || mono > float32(ui)+1 {
		t.Errorf("x-height: text %d, inline code %v", ui, mono)
	}

	win, err := headless.NewWindow(600, 200)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 4, PxPerSp: 4}, Constraints: layout.Exact(image.Pt(600, 200))}
	paint.Fill(gtx.Ops, color.NRGBA{A: 0xff})
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	var rt richtext.InteractiveText
	richtext.Text(&rt, th.Shaper,
		richtext.SpanStyle{Font: th.UIFont, Size: 30, Color: white, Content: "xx"},
		richtext.SpanStyle{Font: th.MonoFont, Size: 30 * unit.Sp(k), Color: white, Content: "xx"},
	).Layout(gtx)
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 600, 200))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	// The lowest lit row in each half is that run's baseline.
	base := func(x0, x1 int) int {
		for y := 199; y >= 0; y-- {
			for x := x0; x < x1; x++ {
				if img.RGBAAt(x, y).R > 0x80 {
					return y
				}
			}
		}
		return -1
	}
	lo, hi := -1, -1 // the run's lit columns: text first, code last
	for x := range 600 {
		for y := range 200 {
			if img.RGBAAt(x, y).R > 0x80 {
				if lo < 0 {
					lo = x
				}
				hi = x
				break
			}
		}
	}
	text, code := base(lo, lo+20), base(hi-20, hi+1)
	if text < 0 || code < 0 || text-code > 1 || code-text > 1 {
		t.Errorf("baselines: text %d, inline code %d", text, code)
	}
}

// A URL in inline code stays code; a bare one still becomes a link.
func TestCodeURL(t *testing.T) {
	r := markdown.NewRenderer()
	linkCol := color.NRGBA{B: 0xff, A: 0xff}
	r.Config.InteractiveColor = linkCol
	spans, err := r.Render([]byte("Run `curl -fsSL https://example.com/get.sh | sh`, or read https://example.com/docs"))
	if err != nil {
		t.Fatal(err)
	}
	var code, link string
	for _, s := range spans {
		if strings.Contains(s.Content, "[") {
			t.Errorf("markdown leaked into %q", s.Content)
		}
		if strings.Contains(s.Content, "get.sh") {
			code = s.Content
		}
		if s.Color == linkCol {
			link = s.Content
		}
	}
	if code != "curl -fsSL https://example.com/get.sh | sh" || link != "https://example.com/docs" {
		t.Errorf("code %q, link %q", code, link)
	}
}

// Punctuation after bold or code travels with it, so it never wraps alone.
func TestGluePunct(t *testing.T) {
	sp := func(s ...string) []richtext.SpanStyle {
		out := make([]richtext.SpanStyle, len(s))
		for i := range s {
			out[i].Content = s[i]
		}
		return out
	}
	text := func(ss []richtext.SpanStyle) []string {
		out := []string{}
		for _, s := range ss {
			out = append(out, s.Content)
		}
		return out
	}
	for _, c := range []struct{ in, want []string }{
		{[]string{"moved under ", "Build from source", "."}, []string{"moved under ", "Build from source."}},
		{[]string{"in ", "Add", ", ", "Remove", " and ", "Total", ". Then"}, []string{"in ", "Add,", " ", "Remove", " and ", "Total.", " Then"}},
		{[]string{"ends with space ", ". odd"}, []string{"ends with space ", ". odd"}},
		{[]string{"x", "plain"}, []string{"x", "plain"}},
	} {
		if got := text(gluePunct(sp(c.in...))); !slices.Equal(got, c.want) {
			t.Errorf("gluePunct(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
