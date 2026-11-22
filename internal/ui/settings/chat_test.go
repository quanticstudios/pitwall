package settings

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestChatRows: Appearance has the chat code size under the chat text
// size, and a Chat section with the width and the tool calls, each on the
// value config.toml holds.
func TestChatRows(t *testing.T) {
	var p Page
	p.s.Chat.Width, p.s.Chat.ToolCalls = config.ChatComfortable, config.ToolsZen
	var labels []string
	for _, sec := range p.appearance() {
		for _, r := range sec.rows {
			labels = append(labels, sec.title+"/"+r.label)
		}
	}
	for _, want := range []string{"Text/Chat text size", "Text/Chat code size", "Chat/Chat layout", "Chat/Chat width", "Chat/Tool calls in chat"} {
		if !slices.Contains(labels, want) {
			t.Errorf("no %s in %q", want, labels)
		}
	}
	if i, j := slices.Index(labels, "Text/Chat text size"), slices.Index(labels, "Text/Chat code size"); j != i+1 {
		t.Errorf("code size at %d, text size at %d: want it right under", j, i)
	}
	for dp, want := range map[int]string{config.ChatComfortable: "comfortable", config.ChatStandard: "standard", 0: "full", 1234: "standard"} {
		if got := chatWidthName(dp); got != want {
			t.Errorf("chatWidthName(%d) = %q, want %q", dp, got, want)
		}
	}
}

// TestChatSettingsShot writes the Appearance page to
// $PITWALL_TIMELINE_SHOTS; it skips without it.
func TestChatSettingsShot(t *testing.T) {
	dir := os.Getenv("PITWALL_TIMELINE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TIMELINE_SHOTS is not set")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[chat]\ntool_calls = \"failed\"\nwidth = \"comfortable\"\n[font]\nchat_code_size = 12\n"), 0o600)
	s, _ := config.LoadFile(path)
	var p Page
	p.Show(path)
	size := image.Pt(1100, 1500)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for range 3 {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), s, nil)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(filepath.Join(dir, "settings-appearance.png"))
	defer f.Close()
	png.Encode(f, img)
}
