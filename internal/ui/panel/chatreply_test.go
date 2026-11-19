package panel

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// runAt is the styled run of p that covers the first rune of sub, as
// text, or false.
func runAt(p para, sub string) (srun, bool) {
	i := strings.Index(p.text, sub)
	if i < 0 {
		return srun{}, false
	}
	n := len([]rune(p.text[:i]))
	for _, r := range p.runs {
		if n >= r.lo && n < r.hi {
			return r, true
		}
	}
	return srun{}, false
}

// TestReplySpans: bold, italic, both, struck, code, code in bold and code
// in a link each get a run of their own, links their targets, and the
// plain words none.
func TestReplySpans(t *testing.T) {
	src := "Plain **bold** *ital* ***both*** ~~gone~~ `code` **run `go test` now** [the `Parse` func](https://x.dev) end"
	b := mdview.Parse([]byte(src))
	if len(b) != 1 {
		t.Fatalf("%d blocks", len(b))
	}
	p := spansPara(b[0].Spans, func(link string, code bool) string {
		if code {
			return ""
		}
		return link
	})
	for _, c := range []struct {
		sub                        string
		bold, italic, mono, strike bool
	}{
		{"bold", true, false, false, false},
		{"ital", false, true, false, false},
		{"both", true, true, false, false},
		{"gone", false, false, false, true},
		{"code", false, false, true, false},
		{"run ", true, false, false, false},
		{"go test", true, false, true, false},
		{"Parse", false, false, true, false},
	} {
		r, ok := runAt(p, c.sub)
		if !ok || r.s.Bold != c.bold || r.s.Italic != c.italic || r.s.Mono != c.mono || r.s.Strike != c.strike {
			t.Errorf("%q: run %+v (found %v)", c.sub, r.s, ok)
		}
	}
	for _, sub := range []string{"Plain", "end"} {
		if r, ok := runAt(p, sub); ok {
			t.Errorf("%q is plain, has run %+v", sub, r.s)
		}
	}
	if len(p.links) != 3 || p.hrefs[0] != "https://x.dev" {
		t.Fatalf("links %v %q", p.links, p.hrefs)
	}
	rs := []rune(p.text)
	if got := string(rs[p.links[0][0]:p.links[2][1]]); strings.ReplaceAll(got, nbsp, "") != "the Parse func" {
		t.Errorf("link text %q", got)
	}
	if p.plain != "Plain bold ital both gone code run go test now the Parse func end" {
		t.Errorf("plain %q", p.plain)
	}
}

// TestReplyLists: nested lists keep their depth and markers, a second
// list after a first is marked as one, and the gaps follow: 4 between
// items of a list, 10 before a new list.
func TestReplyLists(t *testing.T) {
	src := "- one\n  - nested\n    1. third\n- back\n\n1. first\n2. second\n\n- [x] done\n"
	b := mdview.Parse([]byte(src))
	want := []struct {
		indent  int
		marker  string
		newList bool
	}{{1, "•", true}, {2, "•", true}, {3, "1.", true}, {1, "•", false}, {1, "1.", true}, {1, "2.", false}, {1, "•", true}}
	if len(b) != len(want) {
		t.Fatalf("%d blocks, want %d", len(b), len(want))
	}
	for i, w := range want {
		if b[i].Indent != w.indent || b[i].Marker != w.marker || b[i].NewList != w.newList {
			t.Errorf("block %d: indent %d marker %q new %v, want %+v", i, b[i].Indent, b[i].Marker, b[i].NewList, w)
		}
	}
	if b[6].Task != 2 {
		t.Errorf("task %d", b[6].Task)
	}
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	for i, g := range map[int]unit.Dp{1: gapItem, 2: gapItem, 3: gapItem, 4: gapPara, 5: gapItem, 6: gapPara} {
		if got := gapBefore(gtx, b, i); got != int(g) {
			t.Errorf("gap before block %d: %d, want %v", i, got, g)
		}
	}
}

// TestReplyHref: a link opens as written, a path in code opens when the
// file is under the chat's folder, and code that is not a path does not.
func TestReplyHref(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tr := Transcript{Dir: dir}
	file := mdview.FileURL(filepath.Join(dir, "a", "b.go"))
	for _, c := range []struct {
		link string
		code bool
		want string
	}{
		{"https://x.dev/a", false, "https://x.dev/a"},
		{"a/b.go", false, file},
		{"a/b.go:47", true, file},
		{"a/b.go:47:3", true, file},
		{"a/missing.go", true, ""},
		{"go test ./...", true, ""},
		{"https://x.dev", true, ""},
		{"#heading", false, ""},
	} {
		if got := tr.href(c.link, c.code); got != c.want {
			t.Errorf("href(%q, %v) = %q, want %q", c.link, c.code, got, c.want)
		}
	}
}

// TestReplyFixture: the fixture of everything Claude writes lays out as a
// finished reply at a desktop width and a narrow one, and every prefix of
// it lays out as a reply still streaming, without a panic. A long code
// line scrolls sideways instead of wrapping.
func TestReplyFixture(t *testing.T) {
	src, err := os.ReadFile("testdata/reply.md")
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Dark()
	turn := "t1"
	snap := func(text string, running bool) *session.Snapshot {
		s := &session.Snapshot{SessionID: "0123456789abcdef", Running: running, Turns: []session.TurnSummary{{ID: turn, State: session.TurnRunning}}}
		if !running {
			s.Turns[0].State = session.TurnCompleted
		}
		s.Content = []session.Item{
			{ID: "p", Kind: session.KindUserPrompt, Text: "markdown", TurnID: &turn},
			{ID: "r", Kind: session.KindAssistantText, Text: text, TurnID: &turn},
		}
		return s
	}
	var ops op.Ops
	frame := func(tr *Transcript, s *session.Snapshot, w int) {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, 4000)), Now: time.Now()}
		tr.Layout(gtx, th, s, false)
	}
	for _, w := range []int{1000, 412} {
		var tr Transcript
		frame(&tr, snap(string(src), false), w)
		r := tr.docs["r"]
		if r == nil || r.live || len(r.blocks) < 20 {
			t.Fatalf("width %d: reply %v", w, r)
		}
		if len(r.wide) == 0 {
			t.Errorf("width %d: no code block laid out", w)
		}
	}
	var tr Transcript
	for n := 0; n <= len(src); n += 7 {
		frame(&tr, snap(string(src[:n]), true), 1000)
	}
	r := tr.docs["r"]
	if r == nil || !r.live {
		t.Fatalf("streaming reply %v", r)
	}
	// A half-written bold span draws bold, an open fence draws as code.
	if p := spansPara(tr.reply(&session.Item{ID: "x", Text: "some **bol"}, true).blocks[0].Spans, nil); len(p.runs) != 1 || !p.runs[0].s.Bold {
		t.Errorf("open bold: runs %+v", p.runs)
	}
	if b := tr.reply(&session.Item{ID: "y", Text: "text\n\n```go\nfunc main() {"}, true).blocks; len(b) != 2 || b[1].Kind != mdview.Code {
		t.Errorf("open fence: %+v", b)
	}
}

// TestReplyLinkOpens: a click on a link in a reply opens its target, and
// a link to a missing file has no click target at all.
func TestReplyLinkOpens(t *testing.T) {
	th := theme.Dark()
	var opened []string
	tr := Transcript{Open: func(link string) { opened = append(opened, link) }}
	s := &session.Snapshot{SessionID: "0123456789abcdef", Content: []session.Item{
		{ID: "r", Kind: session.KindAssistantText, Text: "See [the docs](https://x.dev/docs) and [gone](missing.md)."},
	}}
	var ops op.Ops
	transcriptFrame(&tr, th, s, &ops, time.Now())
	c := tr.clicks["r/0/a0/0"]
	if c == nil {
		t.Fatal("the link has no click target")
	}
	if tr.clicks["r/0/a1/0"] != nil {
		t.Error("a link to a missing file is clickable")
	}
	c.Click()
	transcriptFrame(&tr, th, s, &ops, time.Now())
	if len(opened) != 1 || opened[0] != "https://x.dev/docs" {
		t.Fatalf("opened %q", opened)
	}
}
