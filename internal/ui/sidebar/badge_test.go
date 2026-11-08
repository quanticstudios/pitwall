package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestBadgeContrast: the board button's badge number reads at
// theme.MinContrast on its fill in every built-in theme.
func TestBadgeContrast(t *testing.T) {
	for _, name := range config.Themes {
		c, _ := config.Builtin(name)
		th, err := theme.New(c, config.Font{})
		if err != nil {
			t.Fatal(name, err)
		}
		bg, fg := badgeColors(th)
		if got := theme.Contrast(fg, bg); got < theme.MinContrast {
			t.Errorf("%s: badge contrast %.2f, want %.1f", name, got, theme.MinContrast)
		}
	}
}
