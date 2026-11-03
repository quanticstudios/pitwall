package form

import (
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const deploy = `<form title="Deploy">
  <text>
    Pick a target
    and a note.

    Second paragraph.
  </text>
  <input id="name" label="Name" value="web" placeholder="service"/>
  <input id="note" label="Note" lines="3">first line</input>
  <choice id="env" label="Environment">
    <option>staging</option>
    <option value="prod" selected>Production</option>
  </choice>
  <choice id="checks" label="Checks" multi>
    <option selected="true">lint</option>
    <option>tests</option>
  </choice>
  <button id="go">Deploy &amp; tag</button>
  <button cancel>Cancel</button>
</form>
`

func TestParseForm(t *testing.T) {
	f, err := Parse([]byte(deploy))
	if err != nil {
		t.Fatal(err)
	}
	want := &Form{Title: "Deploy", Items: []Item{
		{Kind: Text, Text: "Pick a target and a note.\nSecond paragraph."},
		{Kind: Input, ID: "name", Label: "Name", Text: "web", Placeholder: "service", Lines: 1},
		{Kind: Input, ID: "note", Label: "Note", Text: "first line", Lines: 3},
		{Kind: Choice, ID: "env", Label: "Environment", Options: []Option{{"staging", "staging", false}, {"prod", "Production", true}}},
		{Kind: Choice, ID: "checks", Label: "Checks", Multi: true, Options: []Option{{"lint", "lint", true}, {"tests", "tests", false}}},
		{Kind: Button, ID: "go", Text: "Deploy & tag"},
		{Kind: Button, ID: "Cancel", Text: "Cancel", Cancel: true},
	}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", f, want)
	}
	if !f.Waits() {
		t.Fatal("a form with fields should wait")
	}
	if b := f.Buttons(); len(b) != 2 || b[0].ID != "go" {
		t.Fatalf("buttons %+v", b)
	}
}

func TestParseDashboard(t *testing.T) {
	f, err := Parse([]byte(`<form title="Build">
  <progress label="Tests" value="30" max="40">30 of 40</progress>
  <progress label="Over" value="150"/>
  <status label="Lint" state="ok">passed</status>
  <status label="Deploy"/>
</form>`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Waits() || len(f.Buttons()) != 0 {
		t.Fatal("a dashboard asks nothing and gets no buttons")
	}
	want := []Item{
		{Kind: Progress, Label: "Tests", Text: "30 of 40", Value: 30, Max: 40},
		{Kind: Progress, Label: "Over", Value: 100, Max: 100},
		{Kind: Status, Label: "Lint", Text: "passed", State: StateOK},
		{Kind: Status, Label: "Deploy", State: StateIdle},
	}
	if !reflect.DeepEqual(f.Items, want) {
		t.Fatalf("got %+v", f.Items)
	}
}

func TestDefaultButtons(t *testing.T) {
	f, err := Parse([]byte(`<form><input id="q"/></form>`))
	if err != nil {
		t.Fatal(err)
	}
	b := f.Buttons()
	if len(b) != 2 || b[0].ID != "submit" || b[0].Cancel || !b[1].Cancel {
		t.Fatalf("buttons %+v", b)
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{``, "the spec is empty"},
		{`<text>hi</text>`, "line 1: the spec must start with <form>, not <text>"},
		{"<form>\n  <input label=\"x\"/>\n</form>", `line 2: <input> needs an id`},
		{"<form>\n<frob/>\n</form>", "line 2: unknown element <frob>; use one of text, input, choice, button, progress, status"},
		{"<form>\n<input id=\"a\" nam=\"b\"/>\n</form>", `line 2: <input> has no attribute "nam"; it takes id, label, value, placeholder, lines`},
		{"<form>\n<input id=\"a\"/>\n<choice id=\"a\"><option>x</option></choice>\n</form>", `line 3: id "a" is used on line 2 too`},
		{"<form>\n<choice id=\"c\">\n</choice>\n</form>", "line 2: <choice> needs at least one <option>"},
		{"<form><choice id=\"c\"><option selected>a</option><option selected>b</option></choice></form>", "select one, or add multi"},
		{"<form>\n<option>x</option>\n</form>", "line 2: <option> goes inside <choice>"},
		{"<form>\n<progress label=\"t\" value=\"lots\"/>\n</form>", `line 2: value="lots" is not a number`},
		{"<form><progress label=\"t\"/></form>", "<progress> needs a value"},
		{"<form>\n\n<status label=\"t\" state=\"green\"/></form>", `line 3: state="green" is not one of idle, run, ok, warn, fail`},
		{"<form>\n<text>open\n</form>", "line 3: <text> is not closed before </form>"},
		{"<form>\n<text>hi</text>", "<form> is not closed"},
		{"<form>hello</form>", "loose text in <form>; wrap it in <text>"},
		{"<form><input id=\"button\"/></form>", `id "button" is taken`},
		{"<form><button/></form>", "<button> needs a label"},
		{"<form><text><b>x</b></text></form>", "<b> inside <text>; <text> holds text only"},
		{"<form></form><form></form>", "<form> after </form>"},
		{"<form><input id=\"a\" lines=\"0\"/></form>", `lines="0" is not a number from 1 to 20`},
		{"<form>" + strings.Repeat(" ", MaxSize) + "</form>", "the limit is 65536"},
	} {
		_, err := Parse([]byte(c.src))
		var fe *Error
		if err == nil || !errors.As(err, &fe) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.src, err, c.want)
		}
	}
}

func TestAnswer(t *testing.T) {
	f, err := Parse([]byte(deploy))
	if err != nil {
		t.Fatal(err)
	}
	got := Answer(f, "go", map[string]string{"name": "api", "note": "a \"quoted\"\nline"},
		map[string]map[string]bool{"env": {"prod": true}, "checks": {"tests": true, "lint": true}})
	want := `{"button":"go","checks":["lint","tests"],"env":"prod","name":"api","note":"a \"quoted\"\nline"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	got = Answer(f, "go", nil, nil)
	want = `{"button":"go","checks":[],"env":null,"name":"","note":""}`
	if got != want {
		t.Fatalf("empty: got %s", got)
	}
}

func TestParagraphs(t *testing.T) {
	if got := paragraphs("\n  a\n  b  c\n\n\n d\n"); got != "a b c\nd" {
		t.Fatalf("got %q", got)
	}
}

// The examples in the docs and the agent skill parse: the ones in code
// blocks and the whole forms in backticks.
func TestDocExamples(t *testing.T) {
	examples := regexp.MustCompile("(?ms)^ *<form.*?</form>|`<form[^`]*</form>`")
	n := 0
	for _, path := range []string{"../../docs/usage.md", "../../docs/agent-skill.md", "../agent/pitwall_skill.md"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.NewReplacer("$1", "12", "$2", "ok").Replace(string(b))
		for _, ex := range examples.FindAllString(s, -1) {
			n++
			if _, err := Parse([]byte(strings.Trim(ex, "`"))); err != nil {
				t.Errorf("%s: %v in\n%s", path, err, ex)
			}
		}
	}
	if n != 4 {
		t.Fatalf("found %d examples, want 4", n)
	}
}
