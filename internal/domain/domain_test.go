package domain_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/adrianbp/kanary-dev/internal/domain"
)

func TestRevisionShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rev  domain.Revision
		want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"1234567", "1234567"},
		{"12345678", "1234567"},
		{"abcdef0123456789", "abcdef0"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.rev), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.rev.Short())
		})
	}
}

func TestStepDecisionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    domain.StepDecision
		want string
	}{
		{domain.DecisionHold, "Hold"},
		{domain.DecisionAdvance, "Advance"},
		{domain.DecisionPromote, "Promote"},
		{domain.DecisionRollback, "Rollback"},
		{domain.StepDecision(99), "Unknown(99)"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.d.String())
		})
	}
}
