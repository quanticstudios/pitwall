//go:build darwin

package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func detectSystemColorScheme() ColorScheme {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleInterfaceStyle").CombinedOutput()
	if err == nil {
		if strings.EqualFold(strings.TrimSpace(string(out)), "dark") {
			return SchemeDark
		}
		return SchemeLight
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		// macOS omits AppleInterfaceStyle when in Light mode.
		return SchemeLight
	}
	return SchemeUnknown
}

// WatchSystemAppearance watches macOS's global appearance preference and calls
// changed whenever light/dark mode flips, until stop closes.
func WatchSystemAppearance(stop <-chan struct{}, changed func()) {
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "Preferences", ".GlobalPreferences.plist")
	stat := func() int64 {
		if fi, err := os.Stat(plist); err == nil {
			return fi.ModTime().UnixNano()
		}
		return 0
	}
	lastScheme := detectSystemColorScheme()
	lastMod := stat()
	ticks := 0
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ticks++
			mod := stat()
			if mod == lastMod && ticks%5 != 0 {
				continue
			}
			lastMod = mod
			if cur := detectSystemColorScheme(); cur != SchemeUnknown && cur != lastScheme {
				lastScheme = cur
				changed()
			}
		}
	}
}
