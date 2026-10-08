package settings

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

// No test reaches the user's own state, config, daemon or agent sessions.
func TestMain(m *testing.M) {
	usageSources = func() []flow.Source { return nil }
	testenv.Main(m)
}
