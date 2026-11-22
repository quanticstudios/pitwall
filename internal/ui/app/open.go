package app

import (
	"errors"
	"fmt"
	"log"
	neturl "net/url"
	"strings"
)

// openLink opens a link the user Ctrl+clicked in a pane with the OS opener.
// The URL goes in as an argument of its own, never as shell text. A file
// link on a Host names a file there, which this machine cannot open.
func openLink(url string) {
	if curHost() != "" && strings.HasPrefix(strings.ToLower(url), "file:") {
		log.Printf("open link: %q is on %s", url, curHost())
		return
	}
	if err := OpenURL(url); err != nil {
		log.Printf("open link: %q", err)
	}
}

// validateOpenURL checks that raw uses an allowed scheme (http, https, file,
// mailto) and contains no control characters or option-like prefix.
func validateOpenURL(raw string) error {
	if raw == "" || strings.HasPrefix(raw, "-") {
		return fmt.Errorf("refusing to open %q: invalid URL", raw)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return fmt.Errorf("refusing to open %q: control character in URL", raw)
		}
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return fmt.Errorf("refusing to open %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return fmt.Errorf("refusing to open %q: missing host", raw)
		}
	case "file":
		if u.Path == "" {
			return fmt.Errorf("refusing to open %q: missing path", raw)
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return errors.New("refusing to open empty mailto URL")
		}
	default:
		return fmt.Errorf("refusing to open %q: scheme %q is not allowed", raw, u.Scheme)
	}
	return nil
}

// OpenURL opens url in the user's default browser, as an argument of its
// own, never as shell text.
func OpenURL(url string) error {
	if err := validateOpenURL(url); err != nil {
		return err
	}
	cmd := openCommand(url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
