//go:build !linux && !darwin

package logind

import (
	"context"
	"errors"
	"io"
)

// InhibitIdle is unsupported outside Linux and macOS.
func InhibitIdle(context.Context) (io.Closer, error) { return nil, errors.ErrUnsupported }
