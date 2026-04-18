// Package errors declares sentinel errors used across Kanary.
//
// Usage pattern (Go in Action, 2nd Ed., Chapter 7 — Errors In Go): callers
// wrap errors with `fmt.Errorf("context: %w", err)` and the top-level
// reconciler uses `errors.Is(err, kerr.ErrRetryable)` to decide whether to
// requeue.
package errors

import "errors"

// Common sentinel errors — stable identifiers callers can check with errors.Is.
var (
	// ErrNotFound is returned when a required related object is missing.
	ErrNotFound = errors.New("object not found")

	// ErrInvalidSpec indicates the CR is malformed in a non-recoverable way.
	ErrInvalidSpec = errors.New("invalid canary spec")

	// ErrUnsupportedProvider is returned when a TrafficProvider / MetricProvider
	// type has no registered implementation.
	ErrUnsupportedProvider = errors.New("unsupported provider")

	// ErrAnalysisFailed is returned when the analyzer determines the canary
	// must be rolled back.
	ErrAnalysisFailed = errors.New("analysis failed")

	// ErrRetryable wraps transient errors. Reconciler requeues when it sees this.
	ErrRetryable = errors.New("retryable")
)

// Retryable wraps err so reconcilers can distinguish transient from permanent
// failures via errors.Is(err, ErrRetryable).
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &retryableError{err: err}
}

type retryableError struct{ err error }

func (r *retryableError) Error() string { return r.err.Error() }
func (r *retryableError) Unwrap() error { return r.err }
func (r *retryableError) Is(target error) bool {
	return target == ErrRetryable
}
