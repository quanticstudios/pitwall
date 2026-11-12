package mdview

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/repoicon"
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
	defer func(d func(netip.Addr) bool) { dialable = d }(dialable)
	dialable = func(netip.Addr) bool { return true } // the server is on loopback
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

// TestReloadDropsOldImages: saving a file while its gallery loads drops
// the queued loads of the old images, and a remote image the file no
// longer shows gives way to the one it shows now, which a full budget
// refused before the save.
func TestReloadDropsOldImages(t *testing.T) {
	defer func(l func(context.Context, Where, int) (image.Image, error), h int) { loadImage, maxHeld = l, h }(loadImage, maxHeld)
	var mu sync.Mutex
	loads := 0
	release := make(chan struct{})
	loadImage = func(ctx context.Context, w Where, _ int) (image.Image, error) {
		mu.Lock()
		loads++
		mu.Unlock()
		<-release
		return image.NewRGBA(image.Rect(0, 0, 100, 100)), nil // 40,000 bytes
	}
	maxHeld = 40000
	f := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(f, []byte("![x](x.png)\n"), 0o600)
	v := View{ctx: context.Background(), path: f}
	settle := func(k string) *picture {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			v.mu.Lock()
			p := v.imgs[k]
			done := p != nil && p.done
			v.mu.Unlock()
			if done {
				return p
			}
		}
		t.Fatalf("%s did not load", k)
		return nil
	}

	for i := range 100 {
		v.picture(Where{Path: fmt.Sprintf("/img/%d.png", i)}, 0)
	}
	if !v.reload(v.ctx, f) {
		t.Fatal("did not reload")
	}
	v.mu.Lock()
	queued := len(v.queue)
	v.mu.Unlock()
	if queued != 0 {
		t.Errorf("%d old images still queued", queued)
	}
	v.picture(Where{Path: "/x.png"}, 0)
	close(release)
	settle("/x.png")
	mu.Lock()
	n := loads
	mu.Unlock()
	if n > maxLoads+1 {
		t.Errorf("%d loads for one image after the save", n)
	}

	v.mu.Lock()
	v.imgs, v.held = nil, 0
	v.mu.Unlock()
	old, cur := Where{URL: "https://example.com/1.png"}, Where{URL: "https://example.com/2.png"}
	v.picture(old, 0)
	settle(old.URL)
	v.picture(cur, 0)
	if p := settle(cur.URL); !strings.Contains(p.err, "at most") {
		t.Fatalf("over budget: %q", p.err)
	}
	os.Chtimes(f, time.Now(), time.Now().Add(time.Second))
	if !v.reload(v.ctx, f) {
		t.Fatal("did not reload")
	}
	v.picture(cur, 0)
	if p := settle(cur.URL); p.err != "" {
		t.Errorf("after the save: %q", p.err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.imgs[old.URL] != nil || v.held != maxHeld {
		t.Errorf("old image kept: %d bytes held", v.held)
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

// TestMermaidCaps: a body with no diagram type is a note, not a panic,
// and a fanout or a long chart over the caps shows as code before its
// edges are made.
func TestMermaidCaps(t *testing.T) {
	for _, src := range []string{";", " ; ", "---\n---\n;"} {
		if g, note := ParseMermaid(src); g != nil || note == "" {
			t.Errorf("%q: graph %v, note %q", src, g, note)
		}
	}
	group := "A" + strings.Repeat(" & A", 999)
	start := time.Now()
	if g, note := ParseMermaid("graph TD\n" + group + " --> " + group); g != nil || !strings.Contains(note, "edges") {
		t.Errorf("fanout: graph %v, note %q", g != nil, note)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("fanout took %v", d)
	}
	var b strings.Builder
	b.WriteString("graph TD\n")
	for i := range MaxNodes {
		fmt.Fprintf(&b, "n%d --> n%d\n", i, i+1)
	}
	if g, note := ParseMermaid(b.String()); g != nil || !strings.Contains(note, "nodes") {
		t.Errorf("long chain: graph %v, note %q", g != nil, note)
	}
	b.Reset()
	b.WriteString("graph TD\n")
	for range MaxEdges + 1 {
		b.WriteString("A --> B\n")
	}
	if g, note := ParseMermaid(b.String()); g != nil || !strings.Contains(note, "edges") {
		t.Errorf("repeated edge: graph %v, note %q", g != nil, note)
	}
}

// FuzzParseMermaid: no input panics the parser or the layout, and a
// drawn chart stays inside the caps.
func FuzzParseMermaid(f *testing.F) {
	for _, s := range []string{";", "", "graph", "graph LR\nA --> B", "flowchart TD\nA[x] -->|y| B{z} -.-> C((w))\nC ==> A & B",
		"graph TD\nA -- yes --> B\nB --o C\nC <--> A\nA --- A", "---\ntitle: x\n---\ngraph RL\nA([s]) & B[(d)] --> C>e]\nsubgraph s\nD\nend",
		"graph BT\nA[\"q\"] -.- B;C:::k --x D"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		g, note := ParseMermaid(src)
		if g == nil {
			if note == "" {
				t.Fatal("no graph and no note")
			}
			return
		}
		if len(g.Nodes) > MaxNodes || len(g.Edges) > MaxEdges {
			t.Fatalf("%d nodes, %d edges", len(g.Nodes), len(g.Edges))
		}
		sizes := make([]image.Point, len(g.Nodes))
		for i := range sizes {
			sizes[i] = image.Pt(40, 20)
		}
		g.Layout(sizes, 10, 20)
	})
}

// TestSVGSafe: an SVG with a <use>, which oksvg could recurse in forever
// or copy out to millions of elements, is not drawn and does not crash
// the process, nor is one nested too deep or with too many elements; a
// plain SVG still draws, and a tall SVG is drawn no larger than MaxSide.
func TestSVGSafe(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20)) // a regression fails fast
	head := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="10" height="10" viewBox="0 0 10 10">`
	laughs := `<defs><rect id="r0" width="1" height="1"/>`
	for i := 1; i < 12; i++ {
		laughs += fmt.Sprintf(`<g id="r%d">`, i) + strings.Repeat(fmt.Sprintf(`<use href="#r%d"/>`, i-1), 10) + `</g>`
	}
	laughs += `</defs><use href="#r11"/>`
	for name, body := range map[string]string{
		"self":      `<defs><g id="a"><use href="#a"/></g></defs><use href="#a"/>`,
		"sibling":   `<defs><g id="a"></g><use href="#a"/></defs><use href="#a"/>`,
		"cycle":     `<defs><g id="a"><use xlink:href="#b"/></g><g id="b"><use xlink:href="#a"/></g></defs><use xlink:href="#a"/>`,
		"twice":     `<defs><g id="a"><rect width="1" height="1"/></g><g id="a"><use href="#a"/></g></defs><use href="#a"/>`,
		"external":  `<use href="other.svg#a"/>`,
		"plain use": `<defs><rect id="r" width="5" height="5"/></defs><use href="#r"/>`,
		"deep":      strings.Repeat("<g>", repoicon.MaxSVGDepth+1) + strings.Repeat("</g>", repoicon.MaxSVGDepth+1),
		"many":      strings.Repeat(`<rect width="1" height="1"/>`, repoicon.MaxSVGElems+1),
		"laughs":    laughs,
	} {
		if img, err := Decode([]byte(head+body+"</svg>"), 100); err == nil {
			t.Errorf("%s: drawn %v", name, img.Bounds())
		}
		if err := repoicon.SVGSafe([]byte(head + body + "</svg>")); strings.Contains(body, "<use") && (err == nil || !strings.Contains(err.Error(), "<use>")) {
			t.Errorf("%s: refused for %v, not its <use>", name, err)
		}
	}
	ok := head + `<g fill="red"><rect width="5" height="5"/><circle cx="7" cy="7" r="2"/></g></svg>`
	if img, err := Decode([]byte(ok), 100); err != nil || img.Bounds().Dx() != 10 {
		t.Errorf("a plain SVG: %v, %v", img, err)
	}
	tall := `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="40000" viewBox="0 0 400 40000"><rect width="400" height="40000"/></svg>`
	if img, err := Decode([]byte(tall), 800); err != nil || img.Bounds().Dy() > MaxSide || img.Bounds().Dx() > MaxSide {
		t.Errorf("tall SVG: %v, %v", img, err)
	}
}

// TestRemoteImagesStayPublic: a download reaches only public addresses,
// checked on the address dialed, so a name that resolves to loopback and
// a redirect to another address are refused too.
func TestRemoteImagesStayPublic(t *testing.T) {
	for a, want := range map[string]bool{
		"93.184.215.14": true, "2606:4700::1": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"100.64.0.1": false, "100.101.102.103": false, "fd7a:115c:a1e0::1": false, "169.254.169.254": false,
		"fe80::1": false, "0.0.0.0": false, "::": false, "::ffff:127.0.0.1": false, "64:ff9b::a00:1": false,
		"fd00:ec2::254": false, "224.0.0.1": false, "255.255.255.255": false,
	} {
		if got := publicAddr(netip.MustParseAddr(a)); got != want {
			t.Errorf("publicAddr(%s) = %v", a, got)
		}
	}

	png := pngBytes(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(png) }))
	defer srv.Close()
	ctx := context.Background()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	for _, u := range []string{srv.URL, "http://localhost:" + port + "/"} {
		if _, err := LoadImage(ctx, Where{URL: u}, 0); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("%s: %v", u, err)
		}
	}

	// Allow 127.0.0.1 alone: a redirect to 127.0.0.2 is refused when
	// dialed, and a redirect loop stops.
	defer func(d func(netip.Addr) bool) { dialable = d }(dialable)
	dialable = func(a netip.Addr) bool { return a == netip.MustParseAddr("127.0.0.1") }
	other, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("no 127.0.0.2:", err)
	}
	var hits atomic.Int32
	go http.Serve(other, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loop" {
			http.Redirect(w, r, "/loop", http.StatusFound)
			return
		}
		http.Redirect(w, r, "http://"+other.Addr().String()+"/x.png", http.StatusFound)
	}))
	defer redir.Close()
	if _, err := LoadImage(ctx, Where{URL: redir.URL + "/go"}, 0); err == nil || !strings.Contains(err.Error(), "not a public address") || hits.Load() != 0 {
		t.Errorf("redirect: %v, %d requests reached it", err, hits.Load())
	}
	if _, err := LoadImage(ctx, Where{URL: redir.URL + "/loop"}, 0); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("loop: %v", err)
	}
}

// TestImageLoadsBounded: however many images a file has, at most
// maxLoads load at once, and the decoded images kept stay under maxHeld:
// a file shown before gives its images up first, and one of the file
// shown that does not fit is not shown.
func TestImageLoadsBounded(t *testing.T) {
	defer func(l func(context.Context, Where, int) (image.Image, error), h int) { loadImage, maxHeld = l, h }(loadImage, maxHeld)
	var mu sync.Mutex
	running, most := 0, 0
	release := make(chan struct{})
	loadImage = func(ctx context.Context, w Where, _ int) (image.Image, error) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return image.NewRGBA(image.Rect(0, 0, 100, 100)), nil // 40,000 bytes
	}
	maxHeld = 3 * 40000
	v := View{ctx: context.Background()}
	settle := func(keys ...string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; {
			v.mu.Lock()
			done := true
			for _, k := range keys {
				done = done && v.imgs[k] != nil && v.imgs[k].done
			}
			v.mu.Unlock()
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%v did not load", keys)
			}
			time.Sleep(time.Millisecond)
		}
	}
	var keys []string
	for i := range 20 {
		k := fmt.Sprintf("/img/%d.png", i)
		keys = append(keys, k)
		v.picture(Where{Path: k}, 0)
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	n := running
	mu.Unlock()
	if n > maxLoads {
		t.Errorf("%d images loading at once", n)
	}
	close(release)
	settle(keys...)
	v.mu.Lock()
	shown := 0
	for _, k := range keys {
		if v.imgs[k].err == "" {
			shown++
		} else if !strings.Contains(v.imgs[k].err, "at most") {
			t.Errorf("%s: %s", k, v.imgs[k].err)
		}
	}
	if shown != 3 || v.held != maxHeld || most > maxLoads {
		t.Errorf("first file: %d shown, %d bytes held, %d at once", shown, v.held, most)
	}
	v.visit++ // another file is shown
	v.mu.Unlock()
	v.picture(Where{Path: "/next.png"}, 0)
	settle("/next.png")
	v.mu.Lock()
	defer v.mu.Unlock()
	if p := v.imgs["/next.png"]; p.err != "" || v.held != maxHeld || len(v.imgs) != 20 {
		t.Errorf("next file: %q, %d bytes held, %d images", p.err, v.held, len(v.imgs))
	}
}

// TestPhoneTable: at a phone's width a table too wide to read keeps its
// columns at least minColumn wide and scrolls sideways inside the column,
// as a long code line does; a table that fits neither scrolls nor
// overflows, and a sideways drag of the wide one moves it.
func TestPhoneTable(t *testing.T) {
	th := theme.Dark()
	var d Doc
	d.Set(Parse([]byte("| Package | What changed | Why | Tests | Notes |\n|-|-|-|-|-|\n" +
		"| internal/ui/mdview | draws tables and code that scroll sideways | phones | TestPhoneTable | none |\n\n" +
		"| a | b |\n|-|-|\n| 1 | 2 |\n\n```\n" + strings.Repeat("x", 300) + "\n```\n")))
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 1<<20)}}
	dims := d.Layout(gtx, th)
	if dims.Size.X != 412 {
		t.Fatalf("width %d", dims.Size.X)
	}
	wide, narrow, code := d.wide[0], d.wide[1], d.wide[2]
	if wide == nil || narrow != nil || code == nil {
		t.Fatalf("sideways scrolls: table %v, small table %v, code %v", wide != nil, narrow != nil, code != nil)
	}
	widths := fitColumns(d.natural[0], 412, int(minColumn))
	for c, w := range widths {
		if w < min(int(minColumn), d.natural[0][c]) {
			t.Errorf("column %d squashed to %d of %d", c, w, d.natural[0][c])
		}
	}
	if sum(widths) <= 412 {
		t.Errorf("columns %v fit in 412 and still scroll", widths)
	}
	// A drag left from the table's middle scrolls it.
	h := d.Block(gtx, th, 0).Size.Y
	var r input.Router
	ops.Reset()
	gtx.Source = r.Source()
	d.Layout(gtx, th)
	r.Frame(&ops)
	at := f32.Pt(300, float32(h/2))
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Position: at, Time: 1})
	for k := 1; k <= 10; k++ {
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Touch, Position: at.Sub(f32.Pt(float32(20*k), 0)), Time: time.Duration(1 + k*10_000_000)})
	}
	r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: at.Sub(f32.Pt(200, 0)), Time: 200_000_000})
	ops.Reset()
	d.Layout(gtx, th)
	if wide.x <= 0 {
		t.Errorf("a drag did not scroll the table: x %d", wide.x)
	}
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// TestFetch: with Fetch the reader opens at once and the file follows,
// links and images go through Fetch with ~/ left for it to read, a file
// that does not come says why, and Back returns to where the file was.
func TestFetch(t *testing.T) {
	files := map[string]string{
		"docs/a.md": "# A\n\n[b](../b.md#part) ![i](i.png)\n" + strings.Repeat("\nline\n", 60),
		"b.md":      "# B\n" + strings.Repeat("\nline\n", 60) + "\n## Part\n\n[home](~/c.md)\n",
	}
	var mu sync.Mutex
	var asked []string
	frames := make(chan struct{}, 64)
	v := View{Invalidate: func() { frames <- struct{}{} }}
	v.Fetch = func(ctx context.Context, p string) ([]byte, error) {
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		if p == "docs/i.png" {
			return pngBytes(t, 4, 2), nil
		}
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	defer v.Close()
	th := theme.Dark()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(412, 800))}
	frame := func() {
		t.Helper()
		select {
		case <-frames:
		case <-time.After(5 * time.Second):
			t.Fatal("no frame")
		}
		ops.Reset()
		v.Body(gtx, th)
	}
	shown := func() string {
		v.mu.Lock()
		defer v.mu.Unlock()
		if len(v.doc) == 0 {
			return ""
		}
		return plain(v.doc[0].Spans)
	}
	if err := v.Open("docs/a.md", ""); err != nil || !v.On() {
		t.Fatalf("Open = %v", err)
	}
	frame()
	frame() // the image
	if shown() != "A" {
		t.Fatalf("shows %q", shown())
	}
	v.mu.Lock()
	p := v.imgs["docs/i.png"]
	v.mu.Unlock()
	if p == nil || !p.done || p.err != "" || p.size != image.Pt(4, 2) {
		t.Errorf("image %+v", p)
	}
	v.scroll = 30
	v.follow("../b.md#part")
	frame()
	if shown() != "B" || v.Path() != "b.md" || v.scroll == 0 || !v.HasBack() {
		t.Errorf("follow: %q %s scroll %d", shown(), v.Path(), v.scroll)
	}
	v.follow("~/c.md")
	frame()
	v.mu.Lock()
	failed := v.failed
	v.mu.Unlock()
	if v.Path() != "~/c.md" || failed == "" {
		t.Errorf("missing file: %s %q", v.Path(), failed)
	}
	v.Back()
	v.Back() // cancels the first Back's fetch
	frame()
	if shown() != "A" || v.Path() != "docs/a.md" || v.scroll != 30 || v.HasBack() {
		t.Errorf("back: %q %s scroll %d", shown(), v.Path(), v.scroll)
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(asked)
	if want := []string{"b.md", "b.md", "docs/a.md", "docs/a.md", "docs/i.png", "~/c.md"}; !slices.Equal(asked, want) {
		t.Errorf("fetched %q, want %q", asked, want)
	}
}

// A table's cells never share a text's styles or click state with the
// blocks after it, however many rows it has.
func TestTableCellKeys(t *testing.T) {
	src := "| a | b |\n|-|-|\n" + strings.Repeat("| cell | cell |\n", 512) + "\nAFTER TABLE\n"
	var d Doc
	d.Set(Parse([]byte(src)))
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 1<<24)}}
	d.Layout(gtx, theme.Dark())
	for _, ss := range d.styles {
		for _, s := range ss {
			if s.Content == "AFTER" {
				return
			}
		}
	}
	t.Error("the paragraph after the table was drawn with a cell's text")
}

// A flowchart is placed once, not again on every frame, and again when
// the scale changes the sizes of its nodes.
func TestGraphPlacedOnce(t *testing.T) {
	var d Doc
	d.Set(Parse([]byte("```mermaid\nflowchart TD\n  A[start] --> B{ok?}\n  B -->|yes| C[done]\n  B --> A\n```\n")))
	g := d.blocks[0].Graph
	if g == nil {
		t.Fatal("no graph")
	}
	th := theme.Dark()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 1<<20)}}
	nodes := func() *image.Rectangle {
		ops.Reset()
		d.Layout(gtx, th)
		return &d.graphs[g].pl.Nodes[0]
	}
	first := nodes()
	if nodes() != first {
		t.Error("the second frame placed the chart again")
	}
	gtx.Metric = unit.Metric{PxPerDp: 2, PxPerSp: 2}
	if nodes() == first {
		t.Error("a new scale kept the old placement")
	}
}
