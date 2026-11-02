//go:build !linux

package logind

import (
	"context"
	"errors"
)

// Watch is for Linux; elsewhere pitwall saves on SIGTERM only.
func Watch(context.Context, func(shutdown bool), func()) error { return errors.ErrUnsupported }
