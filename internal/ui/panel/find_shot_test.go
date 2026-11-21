package panel

import (
	"image"
	"os"
	"path/filepath"
	"testing"
)

// TestFindShots writes PNGs of the chat's find bar with its matches and
// of the Changes panel's find to $PITWALL_TOOL_SHOTS, for a look at them;
// it skips without it.
func TestFindShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	s := toolChat()
	tr := &Transcript{Dir: "/repo"}
	tr.init()
	tr.OpenFind()
	tr.msg.find.SetQuery("tool")
	shoot(t, tr, s, filepath.Join(dir, "find-chat.png"), image.Pt(1100, 800), 1.5)

	tr = &Transcript{}
	tr.init()
	tr.OpenFind()
	tr.msg.find.SetQuery("item 4")
	shoot(t, tr, longChat(60), filepath.Join(dir, "find-chat-long.png"), image.Pt(1100, 800), 1.5)

	tr = &Transcript{Dir: "/repo"}
	tr.init()
	tr.showChanges("", "")
	tr.side.find.Open()
	tr.side.find.SetQuery("toolOpen")
	shoot(t, tr, s, filepath.Join(dir, "find-changes.png"), image.Pt(1400, 900), 1.5)
}
