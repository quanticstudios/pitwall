package session

import "errors"

// Errors a Driver returns; docs/managed-sessions.md ("Errors") says who
// sees each.
var (
	ErrUnsupported = errors.New("the provider cannot do this")
	ErrBusy        = errors.New("a turn is running")
	ErrIdle        = errors.New("no turn is running")
	ErrClosed      = errors.New("the provider process has ended")
)

// ProviderError is the provider refusing a command: a JSON-RPC error, a
// control_response with subtype error.
type ProviderError struct{ Op, Message string }

func (e *ProviderError) Error() string { return e.Op + ": " + e.Message }
