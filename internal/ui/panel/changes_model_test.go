package panel

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

func editItem(id, tool, status string, in map[string]any, diff string, files ...session.ToolFile) session.Item {
	raw, _ := json.Marshal(in)
	return session.Item{ID: id, Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: tool, Status: status, Input: raw, Diff: diff, Files: files}}
}

func changesOf(items []session.Item, dir string) []fileChange {
	return turnChanges(items, 0, len(items), dir, func(it *session.Item) []diffLine { return diffLines(it.Tool) })
}

func TestTurnChangesAggregatesPerFile(t *testing.T) {
	root := t.TempDir()
	a, c := filepath.Join(root, "a.go"), filepath.Join(root, "b", "c.go")
	items := []session.Item{
		editItem("1", "Edit", "completed", map[string]any{"file_path": a}, "@@ -1,2 +1,2 @@\n-x\n+y\n z\n"),
		{ID: "t", Kind: session.KindAssistantText, Text: "and again"},
		editItem("2", "MultiEdit", "completed", map[string]any{"file_path": a, "edits": []map[string]string{
			{"old_string": "q", "new_string": "r\ns"}}}, ""),
		editItem("3", "Edit", "completed", map[string]any{"file_path": c}, "@@ -4 +4 @@\n-old\n+new\n"),
	}
	got := changesOf(items, root)
	if len(got) != 2 {
		t.Fatalf("files = %d, want 2: %+v", len(got), got)
	}
	if a := got[0]; a.path != "a.go" || a.add != 3 || a.del != 2 || len(a.diffs) != 2 || a.created {
		t.Errorf("a.go = %+v, want +3 -2 over 2 edits", a)
	}
	if c := got[1]; c.path != filepath.Join("b", "c.go") || c.add != 1 || c.del != 1 {
		t.Errorf("c.go = %+v, want +1 -1", c)
	}
	add, del := changeTotals(got)
	if add != 4 || del != 3 {
		t.Errorf("totals = +%d -%d, want +4 -3", add, del)
	}
	// The second edit has no hunk header: changeLines heads it.
	ls := changeLines(got[0])
	if ls[0].kind != '@' || !strings.HasPrefix(ls[0].text, "@@") {
		t.Errorf("first line = %+v, want the first edit's hunk", ls[0])
	}
	heads := 0
	for _, l := range ls {
		if l.kind == '@' {
			heads++
		}
	}
	if heads != 2 {
		t.Errorf("hunk headers = %d, want 2", heads)
	}
}

func TestTurnChangesNewFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "new.md")
	items := []session.Item{
		editItem("1", "Write", "completed", map[string]any{"file_path": p, "content": "# Hi\n\nthere\n"}, "",
			session.ToolFile{Path: p, Status: "add"}),
	}
	got := changesOf(items, root)
	if len(got) != 1 || !got[0].created || got[0].add != 3 || got[0].del != 0 || got[0].path != "new.md" {
		t.Fatalf("got %+v, want new.md created +3", got)
	}
}

func TestTurnChangesSkipsFailedAndRunning(t *testing.T) {
	items := []session.Item{
		editItem("1", "Edit", "failed", map[string]any{"file_path": "/repo/a.go", "old_string": "a", "new_string": "b"}, ""),
		editItem("2", "Edit", "running", map[string]any{"file_path": "/repo/b.go", "old_string": "a", "new_string": "b"}, ""),
		editItem("3", "Bash", "completed", map[string]any{"command": "echo hi > c.go"}, ""),
		editItem("4", "Read", "completed", map[string]any{"file_path": "/repo/d.go"}, ""),
	}
	if got := changesOf(items, "/repo"); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
	if got := changesOf(nil, "/repo"); len(got) != 0 {
		t.Fatalf("empty turn: got %+v", got)
	}
}

func TestTurnChangesCodex(t *testing.T) {
	items := []session.Item{
		editItem("1", "Edit", "completed", map[string]any{"changes": []map[string]any{
			{"path": "a.go", "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-package a\n+package b\n"},
			{"path": "n.go", "kind": map[string]any{"type": "add"}, "diff": "package n\n\nfunc N() {}\n"},
		}}, ""),
		editItem("2", "apply_patch", "completed", map[string]any{"input": "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** Add File: z.go\n+package z\n*** End Patch\n"}, ""),
	}
	got := changesOf(items, "/repo")
	if len(got) != 3 {
		t.Fatalf("files = %d, want 3: %+v", len(got), got)
	}
	if got[0].path != "a.go" || got[0].add != 2 || got[0].del != 2 {
		t.Errorf("a.go = %+v, want +2 -2 over two calls", got[0])
	}
	if got[1].path != "n.go" || !got[1].created || got[1].add != 3 {
		t.Errorf("n.go = %+v, want created +3", got[1])
	}
	if got[2].path != "z.go" || !got[2].created || got[2].add != 1 {
		t.Errorf("z.go = %+v, want created +1", got[2])
	}
}

func TestReadPreviewLimits(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", []byte("package a\n"))
	write("doc.md", []byte("# Doc\n"))
	write("bin.dat", []byte("ab\x00cd"))
	write("latin.txt", []byte{0xff, 0xfe, 'a'})
	big := bytes.Repeat([]byte("0123456789abcdef\n"), maxSource/17+100)
	write("big.txt", big)
	var pic bytes.Buffer
	_ = png.Encode(&pic, image.NewNRGBA(image.Rect(0, 0, 4, 3)))
	write("p.png", pic.Bytes())
	huge := make([]byte, maxPicture+1)
	write("huge.png", huge)
	if err := os.WriteFile(filepath.Join(out, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		kind previewKind
		why  string
	}{
		{"a.go", previewText, ""},
		{filepath.Join(root, "a.go"), previewText, ""},
		{"doc.md", previewMD, ""},
		{"bin.dat", previewNone, "Binary"},
		{"latin.txt", previewNone, "Binary"},
		{"p.png", previewPicture, ""},
		{"huge.png", previewNone, "over 5 MiB"},
		{"link.txt", previewNone, "Outside"},
		{"../" + filepath.Base(out) + "/secret.txt", previewNone, "Outside"},
		{filepath.Join(out, "secret.txt"), previewNone, "Outside"},
		{"gone.go", previewNone, "Not on disk"},
		{"sub", previewNone, "folder"},
	}
	for _, c := range cases {
		pv := readPreview(root, c.path)
		if pv.kind != c.kind || !strings.Contains(pv.why, c.why) {
			t.Errorf("%s: kind %d why %q, want kind %d why ~%q", c.path, pv.kind, pv.why, c.kind, c.why)
		}
	}
	pv := readPreview(root, "big.txt")
	if !pv.cut || len(pv.text) > maxSource || !strings.HasSuffix(pv.text, "0123456789abcdef") {
		t.Errorf("big.txt: cut %v len %d, want cut at a line under %d", pv.cut, len(pv.text), maxSource)
	}
	if pv := readPreview("", "a.go"); pv.kind != previewNone {
		t.Errorf("no folder: kind %d, want refused", pv.kind)
	}
	if pv := readPreview(root, "p.png"); pv.img == nil || pv.img.Bounds().Dx() != 4 {
		t.Errorf("p.png: img %v", pv.img)
	}
}

func TestLocalFileRoutesToPanel(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	for _, p := range []string{filepath.Join(root, "a.go"), filepath.Join(out, "b.go")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tr := &Transcript{Dir: root}
	cases := map[string]string{
		"a.go":                      filepath.Join(root, "a.go"),
		filepath.Join(root, "a.go"): filepath.Join(root, "a.go"),
		mdview.FileURL(filepath.Join(root, "a.go")): filepath.Join(root, "a.go"),
		filepath.Join(out, "b.go"):                  "", // outside: opens outside pitwall
		"https://example.com/a.go":                  "",
		"gone.go":                                   "",
		root:                                        "", // a folder
	}
	for in, want := range cases {
		if got := tr.localFile(in); got != want {
			t.Errorf("localFile(%q) = %q, want %q", in, got, want)
		}
	}
	tr.openTarget("a.go")
	if !tr.FilesOpen() || !tr.side.file || tr.side.pv == nil || tr.side.pv.kind != previewText {
		t.Errorf("openTarget(a.go): open %v file %v preview %v, want the File view on a.go", tr.FilesOpen(), tr.side.file, tr.side.pv)
	}
	if !tr.CloseFiles() || tr.FilesOpen() || tr.CloseFiles() {
		t.Error("CloseFiles: want true once, then false")
	}
}

// The File view lexes code in a language the lexers know, and leaves
// other text plain.
func TestFileViewHighlights(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"a.go": "package a\n\nfunc F() int { return 1 }\n", "notes.txt": "plain words\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tr := &Transcript{Dir: dir}
	tr.showFile("a.go")
	if len(tr.side.spans) != 3 || len(tr.side.spans[0]) == 0 {
		t.Fatalf("a.go spans %v: want tokens on its lines", tr.side.spans)
	}
	tr.showFile("notes.txt")
	if tr.side.spans != nil {
		t.Errorf("notes.txt spans %v: want none", tr.side.spans)
	}
}

// A plan Claude Code writes in plan mode is no change to the user's files.
func TestPlanFileIsNoChange(t *testing.T) {
	if !planFile("/home/dev/.claude/plans/plan-add-greeting.md") {
		t.Error("a plan under ~/.claude/plans counts as a change")
	}
	if planFile("/repo/docs/plans/roadmap.md") {
		t.Error("the repo's own plans folder was skipped")
	}
}
