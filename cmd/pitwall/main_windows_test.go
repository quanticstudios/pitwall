package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// TestMain runs pitwall's main instead of the tests when a test re-executes
// this binary as pitwall; main_test.go has the Unix TestMain. No test
// reaches the user's own state, config or daemon.
func TestMain(m *testing.M) {
	if os.Getenv("PITWALL_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	// why: run as pitwall without PITWALL_TEST_MAIN, as startDaemon's
	// "daemon" would, the binary would run every test again in a detached
	// child that holds the build's files open.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintln(os.Stderr, "the pitwall test binary was run as pitwall:", os.Args[1:])
		os.Exit(2)
	}
	testenv.Main(m)
}
