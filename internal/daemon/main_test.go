package daemon

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// No test reaches the user's own state, config or daemon. Local time is
// UTC, which the Codex screens the tests feed assume.
func TestMain(m *testing.M) {
	// why: set once, never swapped back; a daemon's pending save timer
	// reads time.Local after its test returns.
	time.Local = time.UTC
	testenv.Main(m)
}
