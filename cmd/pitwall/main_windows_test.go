package main

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// No test reaches the user's own state, config or daemon; main_test.go has
// the Unix TestMain.
func TestMain(m *testing.M) { testenv.Main(m) }
