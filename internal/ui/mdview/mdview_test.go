package mdview

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestParse(t *testing.T) {
	src := "# Intro\n\nSome **bold** and `code` and [a link](b.md).\n\n" +
		"- one\n- [x] done\n  1. nested\n\n" +
		"> quoted\n\n" +
		"| a | b |\n|:-|-:|\n| 1 | 2 |\n\n" +
		"```go\nfunc x() {}\n```\n\n" +
		"```mermaid\ngraph TD\nA-->B\n```\n\n" +
		"```mermaid\nsequenceDiagram\nA->>B: hi\n```\n\n" +
		"Text ![logo](img/logo.png) more\n\n" +
		"![a](a.svg) ![b](https://x.test/b.png)\n\n" +
		"<p align=\"center\"><img src=\"c.png\" alt=\"C\"></p>\n\n" +
		"---\n\n# Intro\n"
	doc := Parse([]byte(src))
	var kinds []string
	for _, b := range doc {
		kinds = append(kinds, map[Kind]string{Para: "p", Heading: "h", Code: "code", Mermaid: "mermaid", Table: "table", Rule: "hr", Images: "img"}[b.Kind])
	}
	want := "h p p p p p table code mermaid mermaid p img p img img hr h"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("kinds:\n got %s\nwant %s", got, want)
	}
	if doc[0].Slug != "intro" || doc[len(doc)-1].Slug != "intro-1" {
		t.Errorf("slugs %q, %q", doc[0].Slug, doc[len(doc)-1].Slug)
	}
	p := doc[1].Spans
	if !p[1].Bold || !p[3].Mono || p[5].Link != "b.md" || p[5].Text != "a link" {
		t.Errorf("inline: %+v", p)
	}
	if doc[2].Marker != "•" || doc[3].Task != 2 || doc[4].Marker != "1." || doc[4].Indent != 2 {
		t.Errorf("list: %+v %+v %+v", doc[2], doc[3], doc[4])
	}
	if doc[5].Quote != 1 {
		t.Errorf("quote: %+v", doc[5])
	}
	if tb := doc[6]; len(tb.Rows) != 2 || string(tb.Align) != "lr" || tb.Rows[1][1][0].Text != "2" {
		t.Errorf("table: %+v", tb)
	}
	if doc[7].Lang != "go" || doc[7].Text != "func x() {}" {
		t.Errorf("code: %+v", doc[7])
	}
	if doc[8].Graph == nil || doc[9].Graph != nil || !strings.Contains(doc[9].Note, "sequenceDiagram is not drawn") {
		t.Errorf("mermaid: %+v / %+v", doc[8], doc[9])
	}
	if doc[11].Imgs[0].Src != "img/logo.png" || len(doc[13].Imgs) != 2 || doc[14].Imgs[0].Src != "c.png" || doc[14].Imgs[0].Alt != "C" {
		t.Errorf("images: %+v %+v %+v", doc[11], doc[13], doc[14])
	}
}

func TestParseMermaid(t *testing.T) {
	g, note := ParseMermaid(`flowchart LR
  %% a comment
  A[Start here] -->|yes| B{Choose}; B -- no --> C((End))
  B -.-> D(["Stadium, quoted"]) & E
  E ==> A
  classDef x fill:#f00
  C:::x --- A
`)
	if g == nil {
		t.Fatal(note)
	}
	if g.Dir != "LR" || len(g.Nodes) != 5 {
		t.Fatalf("dir %s, nodes %+v", g.Dir, g.Nodes)
	}
	shapes := map[string]Shape{"A": Box, "B": Diamond, "C": Circle, "D": Stadium, "E": Box}
	for _, n := range g.Nodes {
		if shapes[n.ID] != n.Shape {
			t.Errorf("node %s shape %d", n.ID, n.Shape)
		}
	}
	if g.Nodes[0].Label != "Start here" || g.Nodes[3].Label != "Stadium, quoted" {
		t.Errorf("labels %+v", g.Nodes)
	}
	want := []Edge{
		{0, 1, "yes", true, false, false},
		{1, 2, "no", true, false, false},
		{1, 3, "", true, true, false},
		{1, 4, "", true, true, false},
		{4, 0, "", true, false, true},
		{2, 0, "", false, false, false},
	}
	if len(g.Edges) != len(want) {
		t.Fatalf("edges %+v", g.Edges)
	}
	for i := range want {
		if g.Edges[i] != want[i] {
			t.Errorf("edge %d = %+v, want %+v", i, g.Edges[i], want[i])
		}
	}
	for _, c := range []struct{ src, note string }{
		{"pie\n\"a\": 1", "Mermaid pie is not drawn"},
		{"graph TD\nA --> ", "cannot read"},
		{"graph TD\nA -> B", "cannot read"},
		{"", "Empty"},
	} {
		if g, note := ParseMermaid(c.src); g != nil || !strings.Contains(note, c.note) {
			t.Errorf("%q: graph %v, note %q, want %q", c.src, g, note, c.note)
		}
	}
}

func TestLayout(t *testing.T) {
	g, _ := ParseMermaid("graph TD\nA --> B\nA --> C\nB --> D\nC --> D\nA --> D\nD --> A")
	sizes := make([]image.Point, len(g.Nodes))
	for i := range sizes {
		sizes[i] = image.Pt(60, 30)
	}
	pl := g.Layout(sizes, 20, 40)
	a, b, c, d := pl.Nodes[0], pl.Nodes[1], pl.Nodes[2], pl.Nodes[3]
	if !(a.Max.Y <= b.Min.Y && b.Max.Y <= d.Min.Y && b.Min.Y == c.Min.Y) {
		t.Errorf("layers: %v %v %v %v", a, b, c, d)
	}
	for i := range pl.Nodes {
		for j := i + 1; j < len(pl.Nodes); j++ {
			if pl.Nodes[i].Overlaps(pl.Nodes[j]) {
				t.Errorf("nodes %d and %d overlap: %v %v", i, j, pl.Nodes[i], pl.Nodes[j])
			}
		}
		if !pl.Nodes[i].In(image.Rectangle{Max: pl.Size}) {
			t.Errorf("node %d %v outside %v", i, pl.Nodes[i], pl.Size)
		}
	}
	// A to D skips a layer, so it bends through a point in it; D back
	// to A, a cycle, runs from D to A.
	if n := len(pl.Lines[4]); n != 3 {
		t.Errorf("A-->D has %d points", n)
	}
	if l := pl.Lines[5]; l[0].Y < l[len(l)-1].Y {
		t.Errorf("D-->A runs %v", l)
	}

	g, _ = ParseMermaid("graph LR\nA --> B")
	pl = g.Layout([]image.Point{{60, 30}, {60, 30}}, 20, 40)
	if pl.Nodes[0].Max.X > pl.Nodes[1].Min.X || pl.Nodes[0].Min.Y != pl.Nodes[1].Min.Y {
		t.Errorf("LR: %v", pl.Nodes)
	}
}

func TestLinkPath(t *testing.T) {
	dir := filepath.FromSlash("/home/u/repo")
	abs := func(s string) string { return filepath.Clean(filepath.FromSlash(s)) }
	home, _ := os.UserHomeDir()
	fileURL := "file:///srv/notes/a.md#Setup"
	if runtime.GOOS == "windows" {
		dir, fileURL = `C:\repo`, "file:///C:/srv/notes/a.md#Setup"
	}
	for _, c := range []struct {
		link, path, frag string
		ok               bool
	}{
		{"docs/usage.md", filepath.Join(dir, "docs", "usage.md"), "", true},
		{"./README.md:12:3", filepath.Join(dir, "README.md"), "", true},
		{"../other/NOTES.MD#a-b", filepath.Join(filepath.Dir(dir), "other", "NOTES.MD"), "a-b", true},
		{"~/plan.markdown", filepath.Join(home, "plan.markdown"), "", true},
		{"my%20notes.md", filepath.Join(dir, "my notes.md"), "", true},
		{"https://example.com/README.md", "", "", false},
		{"main.go", "", "", false},
		{"mailto:a@example.com", "", "", false},
	} {
		p, f, ok := LinkPath(c.link, dir)
		if p != c.path || f != c.frag || ok != c.ok {
			t.Errorf("%q: %q %q %v, want %q %q %v", c.link, p, f, ok, c.path, c.frag, c.ok)
		}
	}
	p, f, ok := LinkPath(fileURL, dir)
	want := abs("/srv/notes/a.md")
	if runtime.GOOS == "windows" {
		want = `C:\srv\notes\a.md`
	}
	if p != want || f != "Setup" || !ok {
		t.Errorf("file URL: %q %q %v", p, f, ok)
	}
	if runtime.GOOS != "windows" {
		if p, _, ok := LinkPath("/etc/x.md", dir); p != "/etc/x.md" || !ok {
			t.Errorf("absolute: %q %v", p, ok)
		}
	}
}

func TestResolveImage(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		src  string
		want Where
		err  bool
	}{
		{"img/a.png", Where{Path: filepath.Join(dir, "img", "a.png")}, false},
		{"a%20b.png?raw=1#x", Where{Path: filepath.Join(dir, "a b.png")}, false},
		{"https://x.test/b.svg", Where{URL: "https://x.test/b.svg"}, false},
		{"data:image/png;base64,AAAA", Where{}, true},
		{"", Where{}, true},
	} {
		w, err := ResolveImage(c.src, dir)
		if w != c.want || (err != nil) != c.err {
			t.Errorf("%q: %+v %v, want %+v", c.src, w, err, c.want)
		}
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDecodeCaps(t *testing.T) {
	img, err := Decode(pngBytes(t, 3000, 10), 0)
	if err != nil || img.Bounds().Dx() != MaxSide {
		t.Errorf("wide PNG: %v, %v", img, err)
	}
	// A PNG whose header claims 10000x10000 is refused before decoding.
	b := pngBytes(t, 1, 1)
	ihdr := b[8+8 : 8+8+13]
	binary.BigEndian.PutUint32(ihdr[0:], 10000)
	binary.BigEndian.PutUint32(ihdr[4:], 10000)
	binary.BigEndian.PutUint32(b[8+8+13:], crc32.ChecksumIEEE(b[8+4:8+8+13]))
	if _, err := Decode(b, 0); err == nil || !strings.Contains(err.Error(), "too many pixels") {
		t.Errorf("huge PNG: %v", err)
	}
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" viewBox="0 0 20 10"><rect width="20" height="10" fill="red"/></svg>`)
	img, err = Decode(svg, 100)
	if err != nil || img.Bounds().Size() != image.Pt(100, 50) {
		t.Errorf("SVG: %v, %v", img, err)
	}
	if _, err := Decode([]byte("not an image"), 0); err == nil {
		t.Error("garbage decoded")
	}

	big := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(big, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapped(big, 32); err == nil {
		t.Error("a file over the cap was read")
	}
}

func TestFetchCaps(t *testing.T) {
	ok := pngBytes(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Write(ok)
		case "/big":
			chunk := make([]byte, 1<<20)
			for range 6 {
				w.Write(chunk)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if img, err := LoadImage(ctx, Where{URL: srv.URL + "/ok.png"}, 0); err != nil || img.Bounds().Dx() != 2 {
		t.Errorf("ok: %v %v", img, err)
	}
	if _, err := LoadImage(ctx, Where{URL: srv.URL + "/big"}, 0); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("big: %v", err)
	}
	if _, err := LoadImage(ctx, Where{URL: srv.URL + "/missing"}, 0); err == nil {
		t.Error("a 404 loaded")
	}
}

// TestReload checks an open file is read again when it changes, and only
// then, and that links between files keep a history to go back through.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	os.WriteFile(a, []byte("# A\n\nsee [b](b.md#part)\n"), 0o600)
	os.WriteFile(b, []byte("# B\n\n## Part\n"), 0o600)
	var v View
	if err := v.Open(a, ""); err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	ctx := v.ctx
	if v.reload(ctx, a) {
		t.Error("reloaded an unchanged file")
	}
	v.scroll = 120
	os.WriteFile(a, []byte("# A, edited\n\nsee [b](b.md#part)\n"), 0o600)
	os.Chtimes(a, time.Now(), time.Now().Add(time.Second))
	if !v.reload(ctx, a) {
		t.Fatal("did not reload a changed file")
	}
	if got := plain(v.doc[0].Spans); got != "A, edited" || v.scroll != 120 {
		t.Errorf("after reload: %q, scroll %d", got, v.scroll)
	}

	v.follow("b.md#part")
	if v.Path() != b || v.anchor != "part" || len(v.history) != 1 || v.scroll != 0 {
		t.Errorf("follow: %s %q %d %d", v.Path(), v.anchor, len(v.history), v.scroll)
	}
	if v.reload(ctx, a) {
		t.Error("the old file's watcher reloaded over the new one")
	}
	v.Back()
	if v.Path() != a || v.scroll != 120 || len(v.history) != 0 {
		t.Errorf("back: %s %d %d", v.Path(), v.scroll, len(v.history))
	}

	var opened []string
	v.OpenURL = func(u string) { opened = append(opened, u) }
	v.follow("https://example.com")
	v.follow("javascript:alert(1)")
	v.follow("LICENSE")
	if len(opened) != 2 || opened[0] != "https://example.com" || !strings.HasSuffix(opened[1], "/LICENSE") || !strings.HasPrefix(opened[1], "file://") {
		t.Errorf("opened %q", opened)
	}
	if err := v.Open(filepath.Join(dir, "missing.md"), ""); err == nil {
		t.Error("opened a missing file")
	}
}

func TestFitColumns(t *testing.T) {
	if got := fitColumns([]int{50, 60}, 200, 40); got[0] != 50 || got[1] != 60 {
		t.Errorf("fits: %v", got)
	}
	got := fitColumns([]int{50, 400, 600}, 300, 40)
	if got[0] != 50 || got[1] != 125 || got[2] != 125 {
		t.Errorf("shared: %v", got)
	}
}

// TestLayoutDraws lays out a file with every kind of block, measured and
// drawn, twice: Gio panics on an unbalanced op stack.
func TestLayoutDraws(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "i.png"), pngBytes(t, 40, 20), 0o600)
	src := "# T\n\n- [x] a\n- b\n\n> q\n\n| a | b |\n|-|-|\n| `c` | [d](d.md) |\n\n```\ncode\n```\n\n" +
		"```mermaid\ngraph LR\nA -->|x| B{y} --> C((z))\nA -.-> C\n```\n\n```mermaid\npie\n```\n\n![i](i.png) ![m](missing.png)\n\n---\n"
	p := filepath.Join(dir, "a.md")
	os.WriteFile(p, []byte(src), 0o600)
	var v View
	if err := v.Open(p, ""); err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	th := theme.Dark()
	for range 2 {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1.5, PxPerSp: 1.5}, Constraints: layout.Exact(image.Pt(1200, 800))}
		v.Layout(gtx, th)
		time.Sleep(50 * time.Millisecond) // the images load
	}
	if len(v.heights) != len(v.doc) || v.heights[0] == 0 {
		t.Errorf("heights %v for %d blocks", v.heights, len(v.doc))
	}
}
