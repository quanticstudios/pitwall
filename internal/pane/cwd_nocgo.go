//go:build darwin && !cgo

package pane

// procCwd needs libproc through cgo on macOS; without it tabs keep the
// directory they opened in.
func procCwd(int) string { return "" }
