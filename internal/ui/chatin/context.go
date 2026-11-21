package chatin

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// PartKind is what a context part of a draft is.
type PartKind uint8

const (
	PartQuote    PartKind = iota + 1 // a passage of an assistant reply
	PartTerminal                     // lines selected in a terminal pane
	PartPasted                       // a big paste, sent as a text file
)

var partNames = [...]string{PartQuote: "Quote", PartTerminal: "Terminal", PartPasted: "Pasted text"}

// Source is where a quote came from in the transcript: the reply's item
// and the selection's two ends, each a block's key and a rune offset in
// its text, so the chat can scroll back to it.
type Source struct {
	Item           string
	FromKey, ToKey string
	From, To       int
}

// Part is context in a draft besides its images, standing in the text
// as its token: a quote with the user's comment, a terminal excerpt, or
// a paste that goes as a text file.
type Part struct {
	Kind    PartKind
	N       int
	Text    string // the quoted passage, the terminal's lines, the paste
	Comment string // a quote's comment, the user's
	Label   string // a terminal excerpt's command, a paste's file name
	Src     Source // a quote's
}

// Token is p's token in the text: [Quote #1], [Terminal #1] or
// [Pasted text #1].
func (p Part) Token() string { return PartToken(p.Kind, p.N) }

// PartToken is the token of part n of kind k.
func PartToken(k PartKind, n int) string { return "[" + partNames[k] + " #" + strconv.Itoa(n) + "]" }

// Name is p's token without its brackets, as its chip says it.
func (p Part) Name() string { return partNames[p.Kind] + " #" + strconv.Itoa(p.N) }

// MaxQuote is the longest passage, in runes, a quote takes, as T3 Code's
// citations do: a longer one is a paste.
const MaxQuote = 8000

// The paste rules, T3 Code's: a paste of FoldAt bytes or more, or one
// that would take the draft past MessageLimit runes, goes as a text file;
// MaxPasted is the largest file the daemon takes (proto.PastedFileLevel).
const (
	FoldAt       = 32 << 10
	MessageLimit = 120_000
	MaxPasted    = proto.MaxPastedText
)

// Fold reports whether paste, going into a draft of n runes, goes as a
// text file rather than inline.
func Fold(paste string, n int) bool {
	return len(paste) >= FoldAt || n+utf8.RuneCountInString(paste) > MessageLimit
}

// AddPart numbers p within its kind and returns its token to put in the
// text. Numbers only go up within a draft, as images' do.
func (d *Draft) AddPart(p Part) string {
	d.seq[p.Kind]++
	p.N = d.seq[p.Kind]
	d.Parts = append(d.Parts, p)
	return p.Token()
}

// Part is d's part of kind k numbered n, nil when there is none.
func (d *Draft) Part(k PartKind, n int) *Part {
	for i := range d.Parts {
		if d.Parts[i].Kind == k && d.Parts[i].N == n {
			return &d.Parts[i]
		}
	}
	return nil
}

// partToken matches a part's token in a draft's text.
var partToken = regexp.MustCompile(`\[(Quote|Terminal|Pasted text) #([0-9]+)\]`)

func kindOf(name string) PartKind {
	for k, n := range partNames {
		if n == name && k > 0 {
			return PartKind(k)
		}
	}
	return 0
}

// Expand is text as the agent reads it: each quote and terminal token
// replaced by its block, a paragraph of its own. A pasted text token
// stays: the daemon puts its file's path there.
func (d *Draft) Expand(text string) string {
	var b strings.Builder
	cur, sep := 0, false
	for _, m := range partToken.FindAllStringSubmatchIndex(text, -1) {
		n, _ := strconv.Atoi(text[m[4]:m[5]])
		p := d.Part(kindOf(text[m[2]:m[3]]), n)
		if p == nil || p.Kind == PartPasted {
			continue
		}
		head := text[cur:m[0]]
		if sep {
			if head = strings.TrimLeft(head, " \t\n"); head != "" {
				head = "\n\n" + head
			}
		}
		out := strings.TrimRight(b.String()+head, " \t\n")
		b.Reset()
		b.WriteString(out)
		if out != "" {
			b.WriteString("\n\n")
		}
		b.WriteString(p.block())
		cur, sep = m[1], true
	}
	rest := text[cur:]
	if sep {
		if rest = strings.TrimLeft(rest, " \t\n"); rest != "" {
			b.WriteString("\n\n")
		}
	}
	b.WriteString(rest)
	return b.String()
}

// block is p as the agent reads it: a quote as a Markdown quote with the
// comment after it, a terminal excerpt fenced under its command.
func (p Part) block() string {
	switch p.Kind {
	case PartQuote:
		lines := strings.Split(strings.Trim(p.Text, "\n"), "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRight("> "+l, " ")
		}
		s := "> Assistant quote:\n" + strings.Join(lines, "\n")
		if c := strings.TrimSpace(p.Comment); c != "" {
			s += "\n\nComment: " + c
		}
		return s
	case PartTerminal:
		body := strings.Trim(strings.ReplaceAll(p.Text, "\r\n", "\n"), "\n")
		fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
		head := "Terminal output:"
		switch l := strings.TrimSpace(p.Label); {
		case l != "" && !strings.Contains(l, "`"):
			head = "Terminal output of `" + l + "`:"
		case l != "":
			head = "Terminal output of " + l + ":"
		}
		return head + "\n" + fence + "\n" + body + "\n" + fence
	}
	return p.Token()
}

// longestRun is the longest run of r in s.
func longestRun(s string, r rune) int {
	best, n := 0, 0
	for _, c := range s {
		if c == r {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

// Pasted are d's pasted texts as a ChatSend carries them, in the order
// their tokens stand in text: kind pasted-text, which the daemon saves to
// a file the agent reads.
func (d *Draft) Pasted(text string) []session.Attachment {
	var out []session.Attachment
	for _, p := range d.ordered(text) {
		if p.Kind != PartPasted {
			continue
		}
		out = append(out, session.Attachment{ID: "pasted-" + strconv.Itoa(p.N), Kind: "pasted-text",
			Name: p.Label, MimeType: "text/plain", SizeBytes: int64(len(p.Text)), Text: p.Text})
	}
	return out
}

// ordered is d's parts in the order their tokens stand in text.
func (d *Draft) ordered(text string) []Part {
	var out []Part
	for _, m := range partToken.FindAllStringSubmatchIndex(text, -1) {
		n, _ := strconv.Atoi(text[m[4]:m[5]])
		if p := d.Part(kindOf(text[m[2]:m[3]]), n); p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// PastedName is the file name of paste n.
func PastedName(n int) string { return "pasted-text-" + strconv.Itoa(n) + ".txt" }

// FirstLine is s's first line with text, its spaces collapsed.
func FirstLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if f := strings.Join(strings.Fields(l), " "); f != "" {
			return f
		}
	}
	return ""
}

// Flat is s on one line, its runs of space collapsed.
func Flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// Size is n bytes as a file list says it: 812 B, 48 KB, 1.2 MB.
func Size(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n) + " B"
	case n < 1000*1000:
		return strconv.Itoa((n+500)/1000) + " KB"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + " MB"
}

// Lines is how many lines s has, a last line with no newline counted.
func Lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}
