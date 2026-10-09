package daemon

import (
	"path/filepath"
	"testing"
)

// A Windows pane has no foreground group, so the tab follows the folder
// the shell's prompt reports instead.
func TestShellTitleFollowsReportedCwd(t *testing.T) {
	d, lp, _ := openLive(t, 0)
	label := func() string { d.mu.Lock(); defer d.mu.Unlock(); return d.st.Workspaces[0].Label }
	there := filepath.Join(t.TempDir(), "aide")
	lp.setCwd(there)
	waitUntil(t, "label aide", func() bool { return label() == "aide" })
	if got := d.state().Panes[0].Cwd; got != there {
		t.Fatalf("pane cwd %q", got)
	}
}
