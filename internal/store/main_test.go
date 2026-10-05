package store

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// No test reaches the user's own state, config or daemon.
func TestMain(m *testing.M) { testenv.Main(m) }
