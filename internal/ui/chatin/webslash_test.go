package chatin

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestWebSlashPort runs the phone page's port of the / menu (app.js)
// under node and wants what SlashMenu gives on the same drafts: the
// word, the rows in their order, and the first row's pick with its caret.
// No draft has a $word, which the phone leaves to the window.
func TestWebSlashPort(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	b, err := os.ReadFile("../../remote/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(b)
	start, end := strings.Index(source, "function slashWord("), strings.Index(source, "// slashUpdate opens")
	if start < 0 || end < start {
		t.Fatal("cannot locate the web / menu")
	}
	cmds := append(slices.Clone(testCommands),
		session.Command{Name: "usage", Description: "Show session cost", Aliases: []string{"cost", "stats"}},
		session.Command{Name: "review", Description: "Review a pull request"},
		session.Command{Name: "release", Description: "Cut a release and tag it", Source: "project"},
		session.Command{Name: "pdf", Description: "Read and fill PDF forms", Source: "plugin"},
		session.Command{Name: "skill-creator", Description: "Make a skill", Source: "system"},
		session.Command{Name: "ux_review:full", Description: "Review the UI", Source: "user"},
		session.Command{Name: "über", Description: "Größe prüfen", Source: "user"},
		session.Command{Name: "constructor", Description: "a name JavaScript objects have", Source: "project"},
		session.Command{Name: "term", Source: "user", Terminal: true})
	type in struct {
		Text  string         `json:"text"`
		Caret int            `json:"caret"`
		P     string         `json:"p"`
		Used  map[string]int `json:"used"`
	}
	type out struct {
		Start int      `json:"start"`
		Rows  []string `json:"rows"`
		Pick  string   `json:"pick"`
		Caret int      `json:"caret"`
	}
	var ins []in
	texts := []string{"/", "/co", "/CON", "/re", "/rev", "/cost", "/cmpt", "/summarizing", "/ux", "/full", "/zz", "/con", "/üb", "/grö", "/sk",
		"please run /re and then push", "fix /sh\nnext", "use /com", "use /sk", "see /tmp/x", "/a\\b", "and/or", "see https://x.io/a", "é /ü", "run /", ""}
	for _, s := range texts {
		r := []rune(s)
		n := len(r)
		carets := []int{0, 1, 3, n - 3, n}
		for i := range r {
			if r[i] == '/' {
				for i < n && !unicode.IsSpace(r[i]) {
					i++
				}
				carets = append(carets, i, i-1) // the end of each word with a /, and inside it
			}
		}
		for _, caret := range carets {
			for _, p := range []string{"claude", "codex", "antigravity", ""} {
				ins = append(ins, in{s, max(0, min(caret, n)), p, nil})
			}
		}
	}
	ins = append(ins, in{"/co", 3, "claude", map[string]int{"constructor": 3, "config": 1}}, in{"/", 1, "codex", map[string]int{"term": 2}})
	defer func(u map[string]int) { slashUsed = u }(slashUsed)
	want := make([]out, len(ins))
	for i, c := range ins {
		slashUsed = c.Used
		var s SlashMenu
		s.update(c.Text, c.Caret, cmds, model.Provider(c.P), true, false)
		o := out{Rows: names(s.rows)}
		if len(s.rows) > 0 {
			o.Start = s.start
			ed := new(widget.Editor)
			ed.SetText(c.Text)
			s.pick(layout.Context{}, ed, c.Text, s.rows[0].Name)
			_, o.Caret = ed.Selection()
			o.Pick = ed.Text()
		}
		want[i] = o
	}
	input, _ := json.Marshal(map[string]any{"cmds": cmds, "ins": ins})
	script := source[start:end] + `
const { cmds, ins } = JSON.parse(require('node:fs').readFileSync(0, 'utf8'));
console.log(JSON.stringify(ins.map((c) => {
  const r = slashRows(c.text, c.caret, c.p, cmds, new Map(Object.entries(c.used || {})));
  const o = { start: r.rows.length ? r.start : 0, rows: r.rows.map((x) => x.name), pick: '', caret: 0 };
  if (r.rows.length) { const p = slashPick(c.text, r.start, r.rows[0].name, slashMark(r.rows[0], c.p)); o.pick = p.text; o.caret = p.caret; }
  return o;
})));
`
	cmd := exec.Command(node, "-e", script)
	cmd.Stdin = bytes.NewReader(input)
	res, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var got []out
	if err := json.Unmarshal(res, &got); err != nil || len(got) != len(want) {
		t.Fatalf("node said %s: %v", res, err)
	}
	for i := range want {
		same := reflect.DeepEqual(got[i].Rows, want[i].Rows) || len(got[i].Rows) == 0 && len(want[i].Rows) == 0
		if !same || got[i].Start != want[i].Start || got[i].Pick != want[i].Pick || got[i].Caret != want[i].Caret {
			t.Errorf("%+v: web %+v, Go %+v", ins[i], got[i], want[i])
		}
	}
	for _, c := range cmds {
		if n := RowNote(c); n != "" && !strings.Contains(source, "'"+n+"'") {
			t.Errorf("app.js lacks the note %q", n)
		}
	}
}
