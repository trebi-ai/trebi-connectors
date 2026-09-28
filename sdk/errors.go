package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Error codes (data.code of a JSON-RPC error). The daemon outbox acts on
// the code: it retries transient and rate_limited, holds auth_required
// until the session is back, and gives up on the others.
const (
	CodeTransient    = "transient"
	CodeRateLimited  = "rate_limited"
	CodeAuthRequired = "auth_required"
	CodeNotFound     = "not_found"
	CodeInvalid      = "invalid"
	CodeUnsupported  = "unsupported"
	CodePermanent    = "permanent"
)

// JSON-RPC 2.0 numeric codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcServerError    = -32000
)

// Error is a typed protocol error. An adapter returns it from any method
// to control what the daemon does next. Its JSON form is the JSON-RPC
// error object.
type Error struct {
	Code       string
	Message    string
	RetryAfter time.Duration
}

type wireError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitempty"`
}

type errorData struct {
	Code         string `json:"code"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
}

// Transient is a network or provider fault. The daemon retries.
func Transient(msg string) *Error { return &Error{Code: CodeTransient, Message: msg} }

// RateLimited asks the daemon to retry after d.
func RateLimited(msg string, d time.Duration) *Error {
	return &Error{Code: CodeRateLimited, Message: msg, RetryAfter: d}
}

// AuthRequired means the session is not valid.
func AuthRequired(msg string) *Error { return &Error{Code: CodeAuthRequired, Message: msg} }

// NotFound means the room, thread, or message is gone.
func NotFound(msg string) *Error { return &Error{Code: CodeNotFound, Message: msg} }

// Invalid means bad params, such as a text that is too long.
func Invalid(msg string) *Error { return &Error{Code: CodeInvalid, Message: msg} }

// Unsupported means the adapter does not have the feature.
func Unsupported(msg string) *Error { return &Error{Code: CodeUnsupported, Message: msg} }

// Permanent is any other final fault.
func Permanent(msg string) *Error { return &Error{Code: CodePermanent, Message: msg} }

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is matches another *Error by code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// MarshalJSON writes the JSON-RPC error object.
func (e *Error) MarshalJSON() ([]byte, error) {
	code := rpcServerError
	switch e.Code {
	case CodeInvalid:
		code = rpcInvalidParams
	case CodeUnsupported:
		code = rpcMethodNotFound
	}
	return json.Marshal(wireError{
		Code: code, Message: e.Message,
		Data: &errorData{Code: e.Code, RetryAfterMS: e.RetryAfter.Milliseconds()},
	})
}

// UnmarshalJSON reads a JSON-RPC error object. An error with no typed data
// maps by its numeric code.
func (e *Error) UnmarshalJSON(b []byte) error {
	var w wireError
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = Error{Message: w.Message}
	if w.Data != nil && w.Data.Code != "" {
		e.Code = w.Data.Code
		e.RetryAfter = time.Duration(w.Data.RetryAfterMS) * time.Millisecond
		return nil
	}
	switch w.Code {
	case rpcMethodNotFound:
		e.Code = CodeUnsupported
	case rpcInvalidParams, rpcInvalidRequest, rpcParseError:
		e.Code = CodeInvalid
	default:
		e.Code = CodePermanent
	}
	return nil
}

// AsError maps any adapter error to a typed error. A timeout or a network
// fault is transient; every other untyped error is permanent.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) {
		return Transient(err.Error())
	}
	return Permanent(err.Error())
}

// CodeOf returns the typed code of err, or "" when err is not an *Error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
