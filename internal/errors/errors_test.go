package errors_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

func TestRetryable_NilPassthrough(t *testing.T) {
	t.Parallel()
	require.Nil(t, kerr.Retryable(nil))
}

func TestRetryable_IsErrRetryable(t *testing.T) {
	t.Parallel()
	wrapped := kerr.Retryable(fmt.Errorf("transient db error"))
	require.ErrorIs(t, wrapped, kerr.ErrRetryable)
}

func TestRetryable_PreservesMessage(t *testing.T) {
	t.Parallel()
	inner := fmt.Errorf("connection refused")
	wrapped := kerr.Retryable(inner)
	require.EqualError(t, wrapped, "connection refused")
}

func TestRetryable_UnwrapsInnerError(t *testing.T) {
	t.Parallel()
	inner := fmt.Errorf("dial tcp: timeout")
	wrapped := kerr.Retryable(inner)
	require.ErrorIs(t, wrapped, inner)
}

func TestRetryable_DoublyWrapped(t *testing.T) {
	t.Parallel()
	inner := kerr.Retryable(fmt.Errorf("flaky"))
	outer := fmt.Errorf("op failed: %w", inner)
	require.ErrorIs(t, outer, kerr.ErrRetryable)
}

func TestSentinels_AreDistinct(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		kerr.ErrNotFound,
		kerr.ErrInvalidSpec,
		kerr.ErrUnsupportedProvider,
		kerr.ErrAnalysisFailed,
		kerr.ErrRetryable,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			require.False(t, errors.Is(a, b), "%v should not match %v", a, b)
		}
	}
}
