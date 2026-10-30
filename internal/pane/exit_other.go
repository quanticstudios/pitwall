//go:build unix && !linux && !darwin

package pane

import "errors"

func watchExit(int) (exitWatcher, error) {
	return nil, errors.New("process exit watching is unsupported on this platform")
}
