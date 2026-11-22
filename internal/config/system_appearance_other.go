//go:build !linux && !darwin && !windows

package config

func detectSystemColorScheme() ColorScheme { return SchemeUnknown }

// WatchSystemAppearance does nothing on platforms without system appearance detection.
func WatchSystemAppearance(stop <-chan struct{}, changed func()) {}
