//go:build !linux

package config

// WatchOmarchy does nothing off Linux, where Omarchy does not run.
func WatchOmarchy(stop <-chan struct{}, changed func()) {}
