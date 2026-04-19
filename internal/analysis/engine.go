// Package analysis runs MetricChecks against a metrics.Provider and decides
// whether a canary step passes or fails (SPEC.md §4.4).
package analysis

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	"github.com/adrianbp/kanary-dev/internal/metrics"
)

// Result is the outcome of a single analysis run.
type Result struct {
	Passed       bool
	FailedChecks int32
	Report       kanaryv1alpha1.AnalysisReport
}

// Engine runs MetricChecks against a Provider and tracks consecutive failures.
type Engine struct {
	provider metrics.Provider
}

// New returns an Engine backed by provider.
func New(provider metrics.Provider) *Engine {
	return &Engine{provider: provider}
}

// Evaluate queries all metrics in canary.Spec.Analysis.Metrics and returns
// the aggregated Result. FailedChecks is incremented from the current
// status on failure and reset to zero on a full pass.
func (e *Engine) Evaluate(ctx context.Context, canary *kanaryv1alpha1.Canary) (Result, error) {
	checks := canary.Spec.Analysis.Metrics
	results := make([]kanaryv1alpha1.AnalysisResult, 0, len(checks))
	failedThisRun := int32(0)

	for _, check := range checks {
		q := domain.MetricQuery{
			Name:   check.Name,
			Query:  check.Query,
			Window: time.Minute,
		}

		res, err := e.provider.Query(ctx, q)
		if err != nil {
			return Result{}, fmt.Errorf("query %q: %w", check.Name, err)
		}

		passed := withinThreshold(res.Value, check.ThresholdRange)
		if !passed {
			failedThisRun++
		}

		results = append(results, kanaryv1alpha1.AnalysisResult{
			Metric: check.Name,
			Value:  res.Value,
			Passed: passed,
		})
	}

	allPassed := failedThisRun == 0

	// Consecutive failure tracking: reset on full pass, increment on any failure.
	failedChecks := canary.Status.FailedChecks
	if allPassed {
		failedChecks = 0
	} else {
		failedChecks++
	}

	return Result{
		Passed:       allPassed,
		FailedChecks: failedChecks,
		Report: kanaryv1alpha1.AnalysisReport{
			Timestamp: metav1.Now(),
			Results:   results,
		},
	}, nil
}

// ShouldRollback reports whether the consecutive failure count has reached
// the configured limit.
func ShouldRollback(canary *kanaryv1alpha1.Canary) bool {
	max := canary.Spec.Strategy.MaxFailedChecks
	if max <= 0 {
		max = 2
	}
	return canary.Status.FailedChecks > max
}

func withinThreshold(value float64, t kanaryv1alpha1.ThresholdRange) bool {
	if t.Min != nil && value < *t.Min {
		return false
	}
	if t.Max != nil && value > *t.Max {
		return false
	}
	return true
}
