package main

import (
	"fmt"
	"io"

	"github.com/quanticstudios/pitwall/internal/web"
)

const webUsage = `usage:
  pitwall web token --rotate   give web clients a new token; connected ones stay, new ones need it
Turn the web listener on with [web] enabled = true in config.toml; see
docs/web-protocol.md.
`

// runWeb is `pitwall web <cmd>`; it returns the exit status.
func runWeb(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "token" || args[1] != "--rotate" {
		fmt.Fprint(stderr, webUsage)
		return 2
	}
	if _, err := web.Rotate(web.Path()); err != nil {
		fmt.Fprintln(stderr, "pitwall:", err)
		return 1
	}
	fmt.Fprintf(stdout, "New token in %s. Web clients read it there when they connect.\n", web.Path())
	return 0
}
