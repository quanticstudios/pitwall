package e2e_test

import (
	"os"
	"testing"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// No test reaches the user's own state, config or daemon. Run as
// "echoer <name>", the binary is echoer's program instead.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "echoer" {
		runEchoer(os.Args[2])
		os.Exit(0)
	}
	testenv.Main(m)
}
