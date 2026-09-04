// Package aierror defines safe, provider-independent generation failure classes.
package aierror

import (
	"errors"
	"fmt"
)

type Kind string

const (
	KindTransport         Kind = "transport failure"
	KindTimeout           Kind = "provider timeout"
	KindRateLimited       Kind = "provider rate limited"
	KindUnavailable       Kind = "provider temporarily unavailable"
	KindAuthentication    Kind = "provider authentication failed"
	KindPermission        Kind = "provider permission denied"
	KindConfiguration     Kind = "provider configuration rejected"
	KindRefusal           Kind = "response was refused"
	KindContentFiltered   Kind = "response was stopped by content filtering"
	KindOutputTruncated   Kind = "response exceeded output token limit"
	KindMalformedResponse Kind = "provider returned a malformed response"
	KindInvalidOutput     Kind = "provider returned invalid structured output"
)

// Error deliberately exposes only a safe operation and category. The wrapped
// cause is retained for errors.Is/As and internal diagnostics, but is never
// interpolated into Error().
type Error struct {
	Operation  string
	Kind       Kind
	Retryable  bool
	Repairable bool
	cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return "AI generation failed"
	}
	if e.Operation == "" {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %s", e.Operation, e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func Transient(operation string, kind Kind, cause error) error {
	return &Error{Operation: operation, Kind: kind, Retryable: true, cause: cause}
}

func Terminal(operation string, kind Kind, cause error) error {
	return &Error{Operation: operation, Kind: kind, cause: cause}
}

func InvalidOutput(operation string, cause error) error {
	return &Error{
		Operation: operation, Kind: KindInvalidOutput, Repairable: true, cause: cause,
	}
}

func IsRetryable(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && providerErr.Retryable
}

func IsRepairable(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && providerErr.Repairable
}

func IsTerminal(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && !providerErr.Retryable
}
