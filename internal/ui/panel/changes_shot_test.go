package panel

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChangeShots writes PNGs of the change card and the side panel's
// views to $PITWALL_TOOL_SHOTS, for a look at them; it skips without it.
func TestChangeShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	repo := t.TempDir()
	src := "package panel\n\n// toolOpen is whether a tool row starts open: only a failed call does.\nfunc toolOpen(c *session.ToolCall) bool {\n\treturn c != nil && c.Status == \"failed\" // a very long comment that runs past the edge of the box so it scrolls sideways instead of wrapping\n}\n"
	for name, body := range map[string]string{
		"internal/ui/panel/chatmodel.go": strings.Repeat(src, 30),
		"README.md":                      "# pitwall\n\nA native terminal multiplexer.\n\n## Keys\n\n- Alt+T switches tabs.\n- `Esc` closes the panel.\n",
		"docs/tools.md":                  "# Tools\n\nEach tool call draws as a row.\n",
		"bin.dat":                        "a\x00b",
	} {
		p := filepath.Join(repo, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := toolChat()
	for i := range s.Content {
		if c := s.Content[i].Tool; c != nil {
			c.Input = []byte(strings.ReplaceAll(string(c.Input), "/repo", repo))
			for j := range c.Files {
				c.Files[j].Path = strings.ReplaceAll(c.Files[j].Path, "/repo", repo)
			}
		}
	}
	chat := func() *Transcript {
		tr := &Transcript{Dir: repo}
		tr.init()
		return tr
	}
	tr := chat()
	shoot(t, tr, s, filepath.Join(dir, "changes-card.png"), image.Pt(1100, 700), 1.5)

	tr = chat()
	tr.showChanges("", "")
	shoot(t, tr, s, filepath.Join(dir, "changes-panel.png"), image.Pt(1400, 900), 1.5)

	for _, f := range []string{"internal/ui/panel/chatmodel.go", "README.md", "bin.dat", "/etc/hosts"} {
		tr = chat()
		tr.showFile(f)
		name := strings.NewReplacer("/", "-", ".", "-").Replace(strings.TrimPrefix(f, "/"))
		shoot(t, tr, s, filepath.Join(dir, "file-"+name+".png"), image.Pt(1400, 900), 1.5)
	}
}
