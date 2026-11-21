//go:build !linux && !darwin

package ports

import "errors"

func Signal(int, bool, bool) error { return errors.ErrUnsupported }

// Supported reports whether Procs and Signal work on this system.
const Supported = false
