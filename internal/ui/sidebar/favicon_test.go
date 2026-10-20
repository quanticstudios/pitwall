package sidebar

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ico wraps entries, each a PNG or a bitmap of width w, in an ICO file.
func ico(entries [][]byte, widths []int) []byte {
	le := binary.LittleEndian
	b := make([]byte, 6+16*len(entries))
	le.PutUint16(b[2:], 1)
	le.PutUint16(b[4:], uint16(len(entries)))
	for i, e := range entries {
		h := b[6+16*i:]
		h[0] = byte(widths[i])
		le.PutUint32(h[8:], uint32(len(e)))
		le.PutUint32(h[12:], uint32(len(b)))
		b = append(b, e...)
	}
	return b
}

func pngOf(t *testing.T, w int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, w))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bmp32 is a w-by-w 32-bit ICO bitmap of one BGRA color, its AND mask left out.
func bmp32(w int, c color.NRGBA) []byte {
	le := binary.LittleEndian
	b := make([]byte, 40, 40+w*w*4)
	le.PutUint32(b, 40)
	le.PutUint32(b[4:], uint32(w))
	le.PutUint32(b[8:], uint32(2*w))
	le.PutUint16(b[12:], 1)
	le.PutUint16(b[14:], 32)
	for range w * w {
		b = append(b, c.B, c.G, c.R, c.A)
	}
	return b
}

func TestFavicon(t *testing.T) {
	red, blue := color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}
	at := func(img image.Image) color.NRGBA { return color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA) }

	// The largest entry wins, a PNG or a bitmap.
	if img := decodeICO(ico([][]byte{pngOf(t, 16, red), pngOf(t, 32, blue)}, []int{16, 32})); img == nil || img.Bounds().Dx() != 32 || at(img) != blue {
		t.Fatalf("PNG entries: %v", img)
	}
	if img := decodeICO(ico([][]byte{bmp32(16, red)}, []int{16})); img == nil || img.Bounds().Dx() != 16 || at(img) != red {
		t.Fatalf("bitmap entry: %v", img)
	}
	if img := decodeICO(ico([][]byte{bmp32(16, color.NRGBA{G: 255})}, []int{16})); img == nil || at(img).A != 255 {
		t.Fatalf("a bitmap with no alpha draws whole: %v", img)
	}
	if decodeICO([]byte("not an icon")) != nil {
		t.Fatal("garbage decoded")
	}

	write := func(root, rel string, b []byte) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	write(repo, "apps/web/public/favicon.ico", ico([][]byte{pngOf(t, 16, blue)}, []int{16}))
	if img := findFavicon(repo); img == nil || at(img) != blue {
		t.Fatalf("a monorepo app's favicon: %v", img)
	}
	write(repo, "public/favicon.png", pngOf(t, 16, red))
	if img := findFavicon(repo); img == nil || at(img) != red {
		t.Fatalf("the repo's own favicon comes first: %v", img)
	}

	// An SVG draws, the repo's own logo with its gradients and strokes
	// too, and comes before a bitmap in the same folder.
	svg := decodeSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#0000ff"/></svg>`))
	if svg == nil || at(svg) != blue {
		t.Fatalf("SVG: %v", svg)
	}
	logo, err := os.ReadFile("../../../packaging/pitwall.svg")
	if err != nil {
		t.Fatal(err)
	}
	if img := decodeSVG(logo); img == nil || color.NRGBAModel.Convert(img.At(svgPx/2, svgPx/2)).(color.NRGBA).A == 0 {
		t.Fatal("pitwall's logo did not draw")
	}
	// oksvg reads scale(s) as scale(s, 0); a one-factor scale must still draw.
	scaled := decodeSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><g transform="translate(0 0) scale(0.5)"><rect width="20" height="20" fill="#0000ff"/></g></svg>`))
	if scaled == nil || at(scaled) != blue {
		t.Fatalf("scale(0.5) drew nothing: %v", scaled)
	}
	if decodeSVG([]byte("<<<")) != nil {
		t.Fatal("a broken SVG decoded")
	}
	write(repo, "public/favicon.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4 4"><rect width="4" height="4" fill="#00ff00"/></svg>`))
	if img := findFavicon(repo); img == nil || at(img) != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("public/favicon.svg should beat public/favicon.png: %v", img)
	}

	// .pitwall/icon.* is the user's pick and beats the rest.
	write(repo, ".pitwall/icon.png", pngOf(t, 16, color.NRGBA{R: 9, G: 9, B: 9, A: 255}))
	if img := findFavicon(repo); img == nil || at(img).R != 9 {
		t.Fatalf(".pitwall/icon.png should win: %v", img)
	}

	th := theme.Dark()
	v := &view{th: th, st: &model.State{Projects: []model.Project{{ID: "g", Name: "billing", Color: "violet"}}}}
	var s Sidebar
	if s.tabFavicon(v, model.Workspace{Path: repo}) != nil {
		t.Fatal("a folder with no .git got an icon")
	}
	write(repo, ".git/HEAD", []byte("ref: refs/heads/main\n"))
	if f := s.tabFavicon(v, model.Workspace{Path: filepath.Join(repo, "public")}); f == nil || f.src == nil {
		t.Fatal("a tab in a subfolder of the repo got no image")
	}

	// A repo with no icon gets its letter: its group's name and color, else
	// its main checkout's name and a color picked from it.
	bare := filepath.Join(t.TempDir(), "acme-api")
	write(bare, ".git/HEAD", []byte("ref: refs/heads/main\n"))
	if f := s.tabFavicon(v, model.Workspace{Path: bare, RepoRoot: bare, ProjectID: "g"}); f == nil || f.letter != "B" || f.col != th.ProjectColor("violet") {
		t.Fatalf("grouped tile: %+v", f)
	}
	wt := filepath.Join(bare, ".worktrees", "fix-it")
	write(wt, ".git", []byte("gitdir: ../../.git/worktrees/fix-it\n"))
	f := s.tabFavicon(v, model.Workspace{Path: wt, RepoRoot: wt, WorktreeRoot: bare})
	if f == nil || f.letter != "A" || f.col != letterFavicon(th, "acme-api", "").col {
		t.Fatalf("a worktree's tile is the repo's: %+v", f)
	}

	// A shell that moved into another repo shows that repo's icon.
	pane := model.Pane{ID: "p", WorkspaceID: "w", Cwd: repo}
	moved := model.Workspace{ID: "w", Path: bare, RepoRoot: bare, ProjectID: "g", ActiveTab: "t", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}}
	v.st.Panes = []model.Pane{pane}
	var fresh Sidebar // s saw repo before it had .git, and keeps that
	if f := fresh.tabFavicon(v, moved); f == nil || f.src == nil {
		t.Fatalf("a shell that cd'd into a repo with an icon: %+v", f)
	}
	if letterFavicon(th, "--", "") != nil {
		t.Fatal("a name with no letter got a tile")
	}
	if s.groupFavicon(theme.Dark(), model.Project{Root: repo, Kind: model.ProjectGit, Icon: "rocket"}) != nil {
		t.Fatal("a group's own icon lost to the favicon")
	}
}
