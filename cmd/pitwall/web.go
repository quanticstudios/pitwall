package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/web"
)

const webUsage = `usage:
  pitwall web open             open the web client ([web] bundle) in your default browser
  pitwall web token --rotate   give web clients a new token; connected ones stay, new ones need it
Turn the web listener on with [web] enabled = true in config.toml; see
docs/web-protocol.md.
`

// openURL opens a URL in the default browser; a var for tests.
var openURL = app.OpenURL

// runWeb is `pitwall web <cmd>`; it returns the exit status.
func runWeb(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "open":
		return webOpen(stdout, stderr)
	case len(args) == 2 && args[0] == "token" && args[1] == "--rotate":
	default:
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

// webOpen opens the local web client. The URL it opens holds a one-time
// code, good for web.OpenTTL, that the listener trades for a session
// cookie; never the token.
func webOpen(stdout, stderr io.Writer) int {
	fail := func(msg string) int { fmt.Fprintln(stderr, "pitwall:", msg); return 1 }
	s := config.LoadWeb(config.Path())
	if !s.Enabled || s.Bundle == "" {
		return fail("the web client is off: set enabled = true and bundle to the folder of a built aide web client under [web] in config.toml")
	}
	f, err := web.Read(web.Path())
	if err != nil || !strings.HasPrefix(f.URL, "ws://") || !strings.HasSuffix(f.URL, "/ws") {
		return fail("pitwall's background service has not started the web listener; open a pitwall window, or run pitwall daemon")
	}
	base := "http://" + strings.TrimSuffix(strings.TrimPrefix(f.URL, "ws://"), "/ws")
	// A listener with a bundle refuses the bootstrap without a session.
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(base + "/_pitwall/bootstrap")
	if err != nil {
		return fail(fmt.Sprintf("nothing answers at %s (%v); pitwall's background service may not be running", base, err))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return fail(fmt.Sprintf("%s does not serve the web client yet (%s); the service reads [web] again within 2 seconds", base, resp.Status))
	}
	code, err := web.NewCode(web.OpenPath(web.Path()), time.Now())
	if err != nil {
		return fail(err.Error())
	}
	link := base + "/_pitwall/open?code=" + code
	if err := openURL(link); err != nil {
		fmt.Fprintf(stdout, "Could not open a browser (%v). Open this link; it works once, within a minute:\n  %s\n", err, link)
		return 0
	}
	fmt.Fprintf(stdout, "Opened %s/ in your browser.\n", base)
	return 0
}
