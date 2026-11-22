//go:build windows

package config

import (
	"time"

	"golang.org/x/sys/windows/registry"
)

const personalizeKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

func detectSystemColorScheme() ColorScheme {
	k, err := registry.OpenKey(registry.CURRENT_USER, personalizeKey, registry.QUERY_VALUE)
	if err != nil {
		return SchemeUnknown
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return SchemeUnknown
	}
	if v == 0 {
		return SchemeDark
	}
	return SchemeLight
}

// WatchSystemAppearance polls Windows's AppsUseLightTheme registry setting and
// calls changed whenever light/dark mode flips, until stop closes.
func WatchSystemAppearance(stop <-chan struct{}, changed func()) {
	last := detectSystemColorScheme()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if cur := detectSystemColorScheme(); cur != SchemeUnknown && cur != last {
				last = cur
				changed()
			}
		}
	}
}
