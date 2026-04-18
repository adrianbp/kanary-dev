package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
)

// TestDecide exercises the pure decision function for manual mode across
// the whole state lattice. Because decide() has no I/O, this runs in ms.
func TestDecide(t *testing.T) {
	t.Parallel()

	baseSteps := []kanaryv1alpha1.Step{{Weight: 10}, {Weight: 50}, {Weight: 100}}

	tests := []struct {
		name         string
		annotations  map[string]string
		steps        []kanaryv1alpha1.Step
		status       kanaryv1alpha1.CanaryStatus
		wantDecision domain.StepDecision
		wantPhase    kanaryv1alpha1.Phase
		wantWeight   int32
	}{
		{
			name:         "no steps → hold idle",
			steps:        nil,
			wantDecision: domain.DecisionHold,
			wantPhase:    kanaryv1alpha1.PhaseIdle,
		},
		{
			name:         "first observation seeds stable",
			steps:        baseSteps,
			status:       kanaryv1alpha1.CanaryStatus{},
			wantDecision: domain.DecisionHold,
			wantPhase:    kanaryv1alpha1.PhaseIdle,
		},
		{
			name:         "awaiting promotion at step 0",
			steps:        baseSteps,
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "1", CurrentStepIndex: 0},
			wantDecision: domain.DecisionHold,
			wantPhase:    kanaryv1alpha1.PhaseAwaitingPromotion,
			wantWeight:   10,
		},
		{
			name:         "no new revision stays idle until target changes",
			steps:        baseSteps,
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "2", CurrentStepIndex: 0},
			wantDecision: domain.DecisionHold,
			wantPhase:    kanaryv1alpha1.PhaseIdle,
			wantWeight:   0,
		},
		{
			name:         "promote annotation advances step",
			steps:        baseSteps,
			annotations:  map[string]string{kanaryv1alpha1.AnnotationPromote: "true"},
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "1", CurrentStepIndex: 0},
			wantDecision: domain.DecisionAdvance,
			wantPhase:    kanaryv1alpha1.PhaseProgressing,
			wantWeight:   50,
		},
		{
			name:         "promote on last step triggers promote",
			steps:        baseSteps,
			annotations:  map[string]string{kanaryv1alpha1.AnnotationPromote: "true"},
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "1", CurrentStepIndex: 2},
			wantDecision: domain.DecisionPromote,
			wantPhase:    kanaryv1alpha1.PhaseSucceeded,
			wantWeight:   100,
		},
		{
			name:         "abort annotation → rollback",
			steps:        baseSteps,
			annotations:  map[string]string{kanaryv1alpha1.AnnotationAbort: "true"},
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "1", CurrentStepIndex: 1},
			wantDecision: domain.DecisionRollback,
			wantPhase:    kanaryv1alpha1.PhaseRolledBack,
			wantWeight:   0,
		},
		{
			name:         "invalid step index fails safely",
			steps:        baseSteps,
			status:       kanaryv1alpha1.CanaryStatus{StableRevision: "1", CurrentStepIndex: 99},
			wantDecision: domain.DecisionRollback,
			wantPhase:    kanaryv1alpha1.PhaseFailed,
			wantWeight:   0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &CanaryReconciler{}

			canary := &kanaryv1alpha1.Canary{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "x",
					Namespace:   "ns",
					Annotations: tc.annotations,
				},
				Spec: kanaryv1alpha1.CanarySpec{
					Strategy: kanaryv1alpha1.Strategy{
						Mode:  kanaryv1alpha1.StrategyManual,
						Steps: tc.steps,
					},
				},
				Status: tc.status,
			}
			target := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					"deployment.kubernetes.io/revision": "2",
				}},
			}

			decision, phase, weight, reason := r.decide(canary, target)
			require.Equal(t, tc.wantDecision, decision, "decision for %s", tc.name)
			require.Equal(t, tc.wantPhase, phase, "phase for %s", tc.name)
			require.Equal(t, tc.wantWeight, weight, "weight for %s", tc.name)
			require.NotEmpty(t, reason, "reason must be set")
		})
	}
}

// TestRequeueFor sanity-checks the poll-interval helper used by Reconcile.
func TestRequeueFor(t *testing.T) {
	t.Parallel()

	require.Equal(t, requeueProgressing, requeueFor(kanaryv1alpha1.PhaseProgressing))
	require.Equal(t, requeueAwaiting, requeueFor(kanaryv1alpha1.PhaseAwaitingPromotion))
	require.Equal(t, requeueIdle, requeueFor(kanaryv1alpha1.PhaseSucceeded))
}

func TestClearCommandAnnotations(t *testing.T) {
	t.Parallel()

	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				kanaryv1alpha1.AnnotationPromote: "true",
				kanaryv1alpha1.AnnotationAbort:   "true",
				"keep":                           "yes",
			},
		},
	}

	clearCommandAnnotations(canary)

	require.Equal(t, map[string]string{"keep": "yes"}, canary.Annotations)
}
