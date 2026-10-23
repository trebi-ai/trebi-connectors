package cmd

import (
	"context"
	"errors"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Exit codes, as the vncdotool table (docs/usage.md "Exit Status").
const (
	ExitOK         = 0
	ExitUnexpected = 1
	ExitUsage      = 2
	ExitAuth       = 3
	ExitConnect    = 10
	ExitClosed     = 11
	ExitProtocol   = 20
	ExitFailed     = 30
	ExitTimeout    = 40
)

// usageError is a bad command line.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usage(err error) error { return usageError{err} }

// failedError is a command that ran and did not do its work.
type failedError struct{ err error }

func (e failedError) Error() string { return e.err.Error() }
func (e failedError) Unwrap() error { return e.err }

func failed(err error) error { return failedError{err} }

// timeoutError is the end of --timeout. It is context.DeadlineExceeded with
// a message for the user.
type timeoutError struct{}

func (timeoutError) Error() string        { return "the time limit (--timeout) ended" }
func (timeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

func onUsageError(_ context.Context, _ *cli.Command, err error, _ bool) error { return usage(err) }

// ExitCode maps an error to its exit code.
func ExitCode(err error) int {
	var (
		ue usageError
		de *vnc.DialError
		pe *vnc.ProtocolError
		fe failedError
	)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, context.DeadlineExceeded):
		return ExitTimeout
	case errors.As(err, &ue):
		return ExitUsage
	case errors.Is(err, vnc.ErrAuth):
		return ExitAuth
	case errors.As(err, &de):
		return ExitConnect
	case errors.Is(err, vnc.ErrClosed):
		return ExitClosed
	case errors.As(err, &pe):
		return ExitProtocol
	case errors.As(err, &fe), errors.Is(err, session.ErrOffScreen):
		return ExitFailed
	}
	return ExitUnexpected
}
