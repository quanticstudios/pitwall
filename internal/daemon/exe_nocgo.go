//go:build darwin && !cgo

package daemon

// readExe needs libproc through cgo on macOS; without it detection goes by
// comm alone.
func readExe(int) string { return "" }
