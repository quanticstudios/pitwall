package app

import (
	"os"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
)

// The switcher is hidden in builds; its tests run with it shown.
func TestMain(m *testing.M) {
	config.SwitcherHidden = false
	aide, conventional = config.Preset("aide"), config.Preset("conventional")
	os.Exit(m.Run())
}
