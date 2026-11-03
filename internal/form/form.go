// Package form is the markup of `pitwall form`: a small XML subset, in the
// spirit of tuios's TML, for a form or a dashboard that a script or an
// agent shows the user over its tab, and the JSON of the user's answers.
//
//	<form title="Deploy">
//	  <text>Pick a target.</text>
//	  <input id="note" label="Note" lines="3"/>
//	  <choice id="env" label="Environment">
//	    <option>staging</option>
//	    <option selected>production</option>
//	  </choice>
//	  <button>Deploy</button>
//	  <button cancel>Cancel</button>
//	</form>
//
// A dashboard uses text, progress and status, and a script sends it again
// with new values to update it.
package form

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// MaxSize is the largest spec Parse takes, in bytes.
const MaxSize = 64 << 10

// Kind is an element of the markup.
type Kind string

const (
	Text     Kind = "text"
	Input    Kind = "input"
	Choice   Kind = "choice"
	Button   Kind = "button"
	Progress Kind = "progress"
	Status   Kind = "status"
)

// The states a status line takes.
const (
	StateIdle = "idle"
	StateRun  = "run"
	StateOK   = "ok"
	StateWarn = "warn"
	StateFail = "fail"
)

// Form is a parsed spec.
type Form struct {
	Title string
	Items []Item
}

// Item is one element of a form, top to bottom.
type Item struct {
	Kind        Kind
	ID          string   // input, choice, button; a button's is its label when unset
	Label       string   // input, choice, progress, status
	Text        string   // text's paragraphs, an input's starting value, a button's label, a progress or status note
	Placeholder string   // input
	Lines       int      // input: 1 for one line, more for a multi-line field
	Multi       bool     // choice: several options at once
	Options     []Option // choice
	Value, Max  float64  // progress
	State       string   // status
	Cancel      bool     // button: pressing it cancels the form
}

// Option is one option of a choice.
type Option struct {
	Value, Label string
	Selected     bool
}

// Waits reports whether the form asks the user something: it has a
// field or a button. A form without either is a dashboard.
func (f *Form) Waits() bool {
	return slices.ContainsFunc(f.Items, func(it Item) bool { return it.Kind == Input || it.Kind == Choice || it.Kind == Button })
}

// Buttons are the form's buttons, or Submit and Cancel for a form that
// waits and names none.
func (f *Form) Buttons() []Item {
	var out []Item
	for _, it := range f.Items {
		if it.Kind == Button {
			out = append(out, it)
		}
	}
	if len(out) == 0 && f.Waits() {
		out = []Item{{Kind: Button, ID: "submit", Text: "Submit"}, {Kind: Button, ID: "cancel", Text: "Cancel", Cancel: true}}
	}
	return out
}

// attrs is what each element takes besides its text.
var attrs = map[string][]string{
	"form":     {"title"},
	"text":     {},
	"input":    {"id", "label", "value", "placeholder", "lines"},
	"choice":   {"id", "label", "multi"},
	"option":   {"value", "selected"},
	"button":   {"id", "cancel"},
	"progress": {"label", "value", "max"},
	"status":   {"label", "state"},
}

// Error is a problem with a spec, at Line (from 1, 0 for none).
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// Parse reads a spec. Its errors are *Error, each naming the line and
// what to write instead.
func Parse(src []byte) (*Form, error) {
	if len(src) > MaxSize {
		return nil, &Error{Msg: fmt.Sprintf("the spec is %d bytes; the limit is %d", len(src), MaxSize)}
	}
	p := &parser{d: xml.NewDecoder(strings.NewReader(string(src))), ids: map[string]int{}}
	p.d.Strict = false // why: bare attributes (<option selected>) and a stray & are what people write
	return p.form()
}

type parser struct {
	d    *xml.Decoder
	ids  map[string]int // id to the line that used it
	line int            // the line of the last token
}

func (p *parser) fail(format string, a ...any) error {
	return &Error{Line: p.line, Msg: fmt.Sprintf(format, a...)}
}

// next is the next token that is not a comment, a processing instruction
// or a directive, or an error naming the line.
func (p *parser) next() (xml.Token, error) {
	for {
		p.line, _ = p.d.InputPos()
		t, err := p.d.RawToken() // why: Token closes an open element at a stray end tag; the end tags are checked here
		if err == io.EOF {
			return nil, io.EOF
		}
		if err != nil {
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				return nil, &Error{Line: se.Line, Msg: strings.TrimPrefix(se.Msg, "XML syntax error: ")}
			}
			return nil, &Error{Line: p.line, Msg: err.Error()}
		}
		switch t := t.(type) {
		case xml.StartElement, xml.EndElement:
			return t, nil
		case xml.CharData:
			return t.Copy(), nil
		}
	}
}

func (p *parser) form() (*Form, error) {
	var root xml.StartElement
	for {
		t, err := p.next()
		if err == io.EOF {
			return nil, &Error{Msg: `the spec is empty; start it with <form title="...">`}
		}
		if err != nil {
			return nil, err
		}
		if cd, ok := t.(xml.CharData); ok {
			if strings.TrimSpace(string(cd)) != "" {
				return nil, p.fail("text outside <form>; put the whole spec inside <form>...</form>")
			}
			continue
		}
		s, ok := t.(xml.StartElement)
		if !ok || s.Name.Local != "form" {
			return nil, p.fail("the spec must start with <form>, not %s", tokenName(t))
		}
		root = s
		break
	}
	a, err := p.attrs(root)
	if err != nil {
		return nil, err
	}
	f := &Form{Title: oneLine(a["title"])}
	for {
		t, err := p.next()
		if err == io.EOF {
			return nil, p.fail("<form> is not closed; end the spec with </form>")
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.EndElement:
			if t.Name.Local != "form" {
				return nil, p.fail("</%s> without its start tag", t.Name.Local)
			}
			if err := p.rest(); err != nil {
				return nil, err
			}
			return f, nil
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, p.fail("loose text in <form>; wrap it in <text>...</text>")
			}
		case xml.StartElement:
			it, err := p.item(t)
			if err != nil {
				return nil, err
			}
			f.Items = append(f.Items, it)
		}
	}
}

// rest checks that nothing but space follows </form>.
func (p *parser) rest() error {
	for {
		t, err := p.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if cd, ok := t.(xml.CharData); ok && strings.TrimSpace(string(cd)) == "" {
			continue
		}
		return p.fail("%s after </form>; a spec holds one form", tokenName(t))
	}
}

func tokenName(t xml.Token) string {
	switch t := t.(type) {
	case xml.StartElement:
		return "<" + t.Name.Local + ">"
	case xml.EndElement:
		return "</" + t.Name.Local + ">"
	}
	return "text"
}

// attrs checks s's attributes against what its element takes.
func (p *parser) attrs(s xml.StartElement) (map[string]string, error) {
	name := s.Name.Local
	ok := attrs[name]
	out := map[string]string{}
	for _, a := range s.Attr {
		k := a.Name.Local
		if a.Name.Space != "" {
			k = a.Name.Space + ":" + k
		}
		if !slices.Contains(ok, k) {
			if len(ok) == 0 {
				return nil, p.fail("<%s> takes no attributes, not %q", name, k)
			}
			return nil, p.fail("<%s> has no attribute %q; it takes %s", name, k, strings.Join(ok, ", "))
		}
		out[k] = a.Value
	}
	return out, nil
}

var elements = "text, input, choice, button, progress, status"

// item reads one element inside <form>, its start tag s already read.
func (p *parser) item(s xml.StartElement) (Item, error) {
	name := s.Name.Local
	if _, known := attrs[name]; !known || name == "form" || name == "option" {
		switch name {
		case "option":
			return Item{}, p.fail("<option> goes inside <choice>")
		case "form":
			return Item{}, p.fail("<form> inside <form>; a spec holds one form")
		}
		return Item{}, p.fail("unknown element <%s>; use one of %s", name, elements)
	}
	line := p.line
	a, err := p.attrs(s)
	if err != nil {
		return Item{}, err
	}
	it := Item{Kind: Kind(name), ID: a["id"], Label: oneLine(a["label"])}
	if name == "choice" {
		it.Multi = truthy(a, "multi")
		if it.Options, err = p.options(); err != nil {
			return Item{}, err
		}
		if len(it.Options) == 0 {
			return Item{}, &Error{Line: line, Msg: "<choice> needs at least one <option>"}
		}
		if n := countSelected(it.Options); n > 1 && !it.Multi {
			return Item{}, &Error{Line: line, Msg: fmt.Sprintf("<choice> has %d options selected; select one, or add multi", n)}
		}
	} else {
		body, err := p.text(name)
		if err != nil {
			return Item{}, err
		}
		it.Text = body
	}
	p.line = line
	switch it.Kind {
	case Text:
		it.Text = paragraphs(it.Text)
		if it.Text == "" {
			return Item{}, p.fail("<text> is empty; write the text between <text> and </text>")
		}
	case Input:
		it.Placeholder = oneLine(a["placeholder"])
		it.Text = strings.TrimSpace(it.Text) // the body, for a long starting value
		if v, ok := a["value"]; ok {
			it.Text = v
		}
		it.Lines = 1
		if v, ok := a["lines"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 20 {
				return Item{}, p.fail(`lines=%q is not a number from 1 to 20`, v)
			}
			it.Lines = n
		}
	case Button:
		it.Text, it.Cancel = oneLine(it.Text), truthy(a, "cancel")
		if it.Text == "" {
			return Item{}, p.fail("<button> needs a label: <button>Deploy</button>")
		}
		if it.ID == "" {
			it.ID = it.Text
		}
	case Progress:
		it.Text, it.Max = oneLine(it.Text), 100
		if v, ok := a["max"]; ok {
			if it.Max, err = number(v); err != nil || it.Max <= 0 {
				return Item{}, p.fail("max=%q is not a number above 0", v)
			}
		}
		v, ok := a["value"]
		if !ok {
			return Item{}, p.fail(`<progress> needs a value, such as value="40"`)
		}
		if it.Value, err = number(v); err != nil || it.Value < 0 {
			return Item{}, p.fail("value=%q is not a number from 0 to max", v)
		}
		it.Value = min(it.Value, it.Max)
	case Status:
		it.Text, it.State = oneLine(it.Text), a["state"]
		switch it.State {
		case "":
			it.State = StateIdle
		case StateIdle, StateRun, StateOK, StateWarn, StateFail:
		default:
			return Item{}, p.fail("state=%q is not one of idle, run, ok, warn, fail", it.State)
		}
		if it.Label == "" && it.Text == "" {
			return Item{}, p.fail(`<status> needs a label or text: <status label="Tests" state="ok">passed</status>`)
		}
	}
	if it.Kind == Input || it.Kind == Choice || it.Kind == Button {
		if a["id"] == "" && it.Kind != Button {
			return Item{}, p.fail(`<%s> needs an id, the key of its answer: <%s id="name">`, name, name)
		}
		if it.ID == "button" && it.Kind != Button {
			return Item{}, p.fail(`id "button" is taken: the answers name the pressed button with it`)
		}
		if prev, dup := p.ids[it.ID]; dup && it.Kind != Button {
			return Item{}, p.fail("id %q is used on line %d too; give each field its own id", it.ID, prev)
		}
		if it.Kind != Button {
			p.ids[it.ID] = line
		}
	}
	return it, nil
}

// text reads an element's content up to its end tag: text only.
func (p *parser) text(name string) (string, error) {
	var b strings.Builder
	for {
		t, err := p.next()
		if err == io.EOF {
			return "", p.fail("<%s> is not closed; write </%s> or end the tag with />", name, name)
		}
		if err != nil {
			return "", err
		}
		switch t := t.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local != name {
				return "", p.fail("<%s> is not closed before </%s>; write </%s> or end the tag with />", name, t.Name.Local, name)
			}
			return b.String(), nil
		case xml.StartElement:
			return "", p.fail("<%s> inside <%s>; <%s> holds text only", t.Name.Local, name, name)
		}
	}
}

// options reads a choice's options up to </choice>.
func (p *parser) options() ([]Option, error) {
	var out []Option
	seen := map[string]bool{}
	for {
		t, err := p.next()
		if err == io.EOF {
			return nil, p.fail("<choice> is not closed; write </choice> after its options")
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.EndElement:
			if t.Name.Local != "choice" {
				return nil, p.fail("<choice> is not closed before </%s>", t.Name.Local)
			}
			return out, nil
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, p.fail("loose text in <choice>; wrap each option in <option>...</option>")
			}
		case xml.StartElement:
			if t.Name.Local != "option" {
				return nil, p.fail("<%s> inside <choice>; a choice holds <option> elements", t.Name.Local)
			}
			a, err := p.attrs(t)
			if err != nil {
				return nil, err
			}
			body, err := p.text("option")
			if err != nil {
				return nil, err
			}
			o := Option{Label: oneLine(body), Value: a["value"], Selected: truthy(a, "selected")}
			if o.Value == "" {
				o.Value = o.Label
			}
			if o.Label == "" {
				o.Label = o.Value
			}
			if o.Value == "" {
				return nil, p.fail("<option> is empty; write its label: <option>staging</option>")
			}
			if seen[o.Value] {
				return nil, p.fail("option %q is listed twice", o.Value)
			}
			seen[o.Value] = true
			out = append(out, o)
		}
	}
}

func countSelected(os []Option) int {
	n := 0
	for _, o := range os {
		if o.Selected {
			n++
		}
	}
	return n
}

// truthy reports whether attribute k is set and not false: a bare
// attribute reads as its own name.
func truthy(a map[string]string, k string) bool {
	v, ok := a[k]
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "0", "off":
		return false
	}
	return true
}

func number(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err == nil && (math.IsNaN(v) || math.IsInf(v, 0)) {
		err = errors.New("not finite")
	}
	return v, err
}

// oneLine is s with its runs of space, newlines included, as one space.
func oneLine(s string) string { return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ") }

// paragraphs is s with each run of lines as one line and a blank line
// between paragraphs kept as "\n".
func paragraphs(s string) string {
	var out []string
	cur := ""
	for line := range strings.SplitSeq(s, "\n") {
		line = oneLine(line)
		switch {
		case line != "" && cur != "":
			cur += " " + line
		case line != "":
			cur = line
		case cur != "":
			out, cur = append(out, cur), ""
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}

// Answer is the JSON `pitwall form` prints for f when the user presses
// button: "button" names it, each input's id its text, each choice's id
// its option's value, or for a multi choice the list of values in the
// form's order. A choice with nothing picked is null, or [] for multi.
func Answer(f *Form, button string, text map[string]string, picked map[string]map[string]bool) string {
	out := map[string]any{"button": button}
	for _, it := range f.Items {
		switch it.Kind {
		case Input:
			out[it.ID] = text[it.ID]
		case Choice:
			vals := []string{}
			for _, o := range it.Options {
				if picked[it.ID][o.Value] {
					vals = append(vals, o.Value)
				}
			}
			switch {
			case it.Multi:
				out[it.ID] = vals
			case len(vals) > 0:
				out[it.ID] = vals[0]
			default:
				out[it.ID] = nil
			}
		}
	}
	b, _ := json.Marshal(out) // why: strings, lists and nil always marshal
	return string(b)
}
