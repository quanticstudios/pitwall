// Package mdview is the markdown reader: a file a pane links to, drawn in
// a reading column over the panes with its images and mermaid flowcharts.
package mdview

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Kind is what a Block draws as.
type Kind uint8

const (
	Para Kind = iota
	Heading
	Code
	Mermaid // a ```mermaid fence: Graph when drawn, else Code with Note
	Table
	Rule
	Images
)

// Span is a run of inline text with one style.
type Span struct {
	Text                       string
	Bold, Italic, Mono, Strike bool
	Link                       string // the destination as written, "" for none
}

// Img is an image as the markdown names it.
type Img struct {
	Src, Alt, Link string
}

// Block is one thing the column draws, top to bottom.
type Block struct {
	Kind  Kind
	Level int // a heading's level
	// Indent is the list depth, Quote the block quote depth.
	Indent, Quote int
	// Marker is a list item's "•" or "3.", on its first block only.
	Marker string
	// Task is a task list item's box: 0 none, 1 open, 2 done.
	Task  int
	Spans []Span
	// Text and Lang are a code block's.
	Text, Lang string
	// Note says why a Mermaid block shows as code.
	Note  string
	Graph *Graph
	// Rows are a table's cells, the header first; Align is each
	// column's: 'l', 'c', 'r' or 0.
	Rows  [][][]Span
	Align []byte
	Imgs  []Img
	// Slug is a heading's anchor, as GitHub makes it.
	Slug string
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// Parse turns markdown into the blocks the reader draws.
func Parse(src []byte) []Block {
	p := &parser{src: src, slugs: map[string]int{}}
	doc := md.Parser().Parse(text.NewReader(src))
	p.blocks(doc, 0, 0)
	return p.out
}

type parser struct {
	src   []byte
	out   []Block
	slugs map[string]int
	// marker and task go on the next block a list item emits.
	marker string
	task   int
}

func (p *parser) emit(b Block) {
	if p.marker != "" {
		b.Marker, b.Task, p.marker, p.task = p.marker, p.task, "", 0
	}
	p.out = append(p.out, b)
}

func (p *parser) blocks(n ast.Node, indent, quote int) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		p.block(c, indent, quote)
	}
}

func (p *parser) block(n ast.Node, indent, quote int) {
	switch n := n.(type) {
	case *ast.Heading:
		spans, _ := p.inline(n)
		b := Block{Kind: Heading, Level: n.Level, Indent: indent, Quote: quote, Spans: spans}
		b.Slug = p.slug(plain(spans))
		p.emit(b)
	case *ast.Paragraph, *ast.TextBlock:
		p.para(n, indent, quote)
	case *ast.ThematicBreak:
		p.emit(Block{Kind: Rule, Indent: indent, Quote: quote})
	case *ast.FencedCodeBlock:
		lang := strings.ToLower(string(n.Language(p.src)))
		b := Block{Kind: Code, Lang: lang, Text: p.lines(n), Indent: indent, Quote: quote}
		if lang == "mermaid" {
			b.Kind = Mermaid
			b.Graph, b.Note = ParseMermaid(b.Text)
		}
		p.emit(b)
	case *ast.CodeBlock:
		p.emit(Block{Kind: Code, Text: p.lines(n), Indent: indent, Quote: quote})
	case *ast.HTMLBlock:
		p.html(p.lines(n), indent, quote)
	case *ast.Blockquote:
		p.blocks(n, indent, quote+1)
	case *ast.List:
		i := n.Start
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			p.marker = "•"
			if n.IsOrdered() {
				p.marker = strconv.Itoa(i) + "."
				i++
			}
			p.blocks(item, indent+1, quote)
			p.marker = ""
		}
	case *east.Table:
		p.table(n, indent, quote)
	default:
		p.blocks(n, indent, quote)
	}
}

// para emits a paragraph's text, and its images as rows of their own
// where they stand.
func (p *parser) para(n ast.Node, indent, quote int) {
	var items []inl
	p.inlines(n, Span{}, &items)
	var spans []Span
	var imgs []Img
	flush := func() {
		if strings.TrimSpace(plain(spans)) != "" || p.task != 0 {
			p.emit(Block{Kind: Para, Indent: indent, Quote: quote, Spans: trimSpans(spans)})
		}
		if len(imgs) > 0 {
			p.emit(Block{Kind: Images, Indent: indent, Quote: quote, Imgs: imgs})
		}
		spans, imgs = nil, nil
	}
	for _, it := range items {
		switch {
		case it.img != nil:
			if strings.TrimSpace(plain(spans)) != "" {
				flush()
			}
			spans = nil
			imgs = append(imgs, *it.img)
		case len(imgs) > 0 && strings.TrimSpace(it.span.Text) == "":
			// the space between two badges
		default:
			if len(imgs) > 0 {
				flush()
			}
			spans = append(spans, it.span)
		}
	}
	flush()
}

// inl is a span or an image.
type inl struct {
	span Span
	img  *Img
}

func (p *parser) inline(n ast.Node) ([]Span, []Img) {
	var items []inl
	p.inlines(n, Span{}, &items)
	var spans []Span
	var imgs []Img
	for _, it := range items {
		if it.img != nil {
			imgs = append(imgs, *it.img)
			if it.img.Alt != "" {
				spans = append(spans, Span{Text: it.img.Alt, Italic: true})
			}
		} else {
			spans = append(spans, it.span)
		}
	}
	return trimSpans(spans), imgs
}

func (p *parser) inlines(n ast.Node, st Span, out *[]inl) {
	add := func(s Span) { *out = append(*out, inl{span: s}) }
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			s := st
			s.Text = string(c.Segment.Value(p.src))
			switch {
			case c.HardLineBreak():
				s.Text += "\n"
			case c.SoftLineBreak():
				s.Text += " "
			}
			add(s)
		case *ast.String:
			s := st
			s.Text = string(c.Value)
			add(s)
		case *ast.CodeSpan:
			s := st
			s.Mono = true
			p.inlines(c, s, out)
		case *ast.Emphasis:
			s := st
			if c.Level >= 2 {
				s.Bold = true
			} else {
				s.Italic = true
			}
			p.inlines(c, s, out)
		case *east.Strikethrough:
			s := st
			s.Strike = true
			p.inlines(c, s, out)
		case *ast.Link:
			s := st
			s.Link = string(c.Destination)
			p.inlines(c, s, out)
		case *ast.AutoLink:
			s := st
			s.Text = string(c.URL(p.src))
			s.Link = s.Text
			if c.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(s.Link, "mailto:") {
				s.Link = "mailto:" + s.Link
			}
			add(s)
		case *ast.Image:
			*out = append(*out, inl{img: &Img{Src: string(c.Destination), Alt: plain(p.children(c)), Link: st.Link}})
		case *ast.RawHTML:
			raw := p.raw(c)
			if brTag.MatchString(raw) {
				s := st
				s.Text = "\n"
				add(s)
			} else if m := imgSrc.FindStringSubmatch(raw); m != nil {
				*out = append(*out, inl{img: &Img{Src: m[1], Alt: attr(raw, "alt"), Link: st.Link}})
			}
		case *east.TaskCheckBox:
			p.task = 1
			if c.IsChecked {
				p.task = 2
			}
		default:
			p.inlines(c, st, out)
		}
	}
}

func (p *parser) children(n ast.Node) []Span {
	var items []inl
	p.inlines(n, Span{}, &items)
	var spans []Span
	for _, it := range items {
		if it.img == nil {
			spans = append(spans, it.span)
		}
	}
	return spans
}

func (p *parser) raw(n *ast.RawHTML) string {
	var b strings.Builder
	for i := 0; i < n.Segments.Len(); i++ {
		s := n.Segments.At(i)
		b.Write(s.Value(p.src))
	}
	return b.String()
}

func (p *parser) lines(n ast.Node) string {
	var b bytes.Buffer
	l := n.Lines()
	for i := 0; i < l.Len(); i++ {
		s := l.At(i)
		b.Write(s.Value(p.src))
	}
	return strings.TrimRight(b.String(), "\n")
}

var (
	imgSrc  = regexp.MustCompile(`(?i)<img\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
	brTag   = regexp.MustCompile(`(?i)^<br\s*/?>$`)
	tag     = regexp.MustCompile(`<[^>]*>`)
	comment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// attr is the value of name in an HTML tag, "" without one.
func attr(tagText, name string) string {
	m := regexp.MustCompile(`(?i)(?:^|\s)` + name + `\s*=\s*["']([^"']*)["']`).FindStringSubmatch(tagText)
	if m == nil {
		return ""
	}
	return m[1]
}

// html shows an HTML block's images, as READMEs center a logo, and its
// text without the tags. Comments show nothing.
func (p *parser) html(s string, indent, quote int) {
	s = comment.ReplaceAllString(s, "")
	if ms := imgSrc.FindAllStringSubmatchIndex(s, -1); ms != nil {
		var imgs []Img
		for _, m := range ms {
			// the tag runs from the match start to the next >
			end := strings.IndexByte(s[m[0]:], '>')
			t := s[m[0]:]
			if end >= 0 {
				t = t[:end]
			}
			imgs = append(imgs, Img{Src: s[m[2]:m[3]], Alt: attr(t, "alt")})
		}
		p.emit(Block{Kind: Images, Indent: indent, Quote: quote, Imgs: imgs})
		return
	}
	t := strings.Join(strings.Fields(tag.ReplaceAllString(s, " ")), " ")
	if t != "" {
		p.emit(Block{Kind: Para, Indent: indent, Quote: quote, Spans: []Span{{Text: t}}})
	}
}

func (p *parser) table(n *east.Table, indent, quote int) {
	b := Block{Kind: Table, Indent: indent, Quote: quote}
	for _, a := range n.Alignments {
		b.Align = append(b.Align, map[east.Alignment]byte{east.AlignLeft: 'l', east.AlignCenter: 'c', east.AlignRight: 'r'}[a])
	}
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells [][]Span
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			spans, _ := p.inline(cell)
			cells = append(cells, spans)
		}
		b.Rows = append(b.Rows, cells)
	}
	p.emit(b)
}

// slug is a heading's anchor as GitHub makes it: lower case, spaces to
// hyphens, punctuation dropped, a number after a repeat.
func (p *parser) slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r == ' ' || r == '-':
			b.WriteByte('-')
		case r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r > 0x7f:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if n := p.slugs[out]; n > 0 {
		p.slugs[out] = n + 1
		return out + "-" + strconv.Itoa(n)
	}
	p.slugs[out] = 1
	return out
}

// plain is spans' text.
func plain(spans []Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// trimSpans drops the line breaks and spaces a paragraph's source left
// at its ends.
func trimSpans(spans []Span) []Span {
	for len(spans) > 0 && strings.TrimSpace(spans[0].Text) == "" {
		spans = spans[1:]
	}
	for len(spans) > 0 && strings.TrimSpace(spans[len(spans)-1].Text) == "" {
		spans = spans[:len(spans)-1]
	}
	if len(spans) > 0 {
		spans[0].Text = strings.TrimLeft(spans[0].Text, " \n")
		spans[len(spans)-1].Text = strings.TrimRight(spans[len(spans)-1].Text, " \n")
	}
	return spans
}
