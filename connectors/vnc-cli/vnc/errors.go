package vnc

import (
	"errors"
	"fmt"
)

var (
	// ErrAuth matches each login failure (errors.Is).
	ErrAuth = errors.New("login failed")
	// ErrClosed is the error after Close, or when the server ends the connection.
	ErrClosed = errors.New("connection closed")
)

// AuthError is a login failure. Reason comes from the server or from the
// client; it never holds the password.
type AuthError struct{ Reason string }

func (e *AuthError) Error() string { return "login failed: " + e.Reason }
func (e *AuthError) Unwrap() error { return ErrAuth }

// DialError is a failure to reach the server.
type DialError struct {
	Addr string
	Err  error
}

func (e *DialError) Error() string { return fmt.Sprintf("connect to %s: %v", e.Addr, e.Err) }
func (e *DialError) Unwrap() error { return e.Err }

// ProtocolError is data from the server that the client cannot use.
type ProtocolError struct {
	Step string
	Err  error
}

func (e *ProtocolError) Error() string { return fmt.Sprintf("%s: %v", e.Step, e.Err) }
func (e *ProtocolError) Unwrap() error { return e.Err }

func protoErr(step string, format string, a ...any) error {
	return &ProtocolError{Step: step, Err: fmt.Errorf(format, a...)}
}
