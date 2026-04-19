package analysis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/analysis"
	"github.com/adrianbp/kanary-dev/internal/domain"
)

// stubProvider returns a configurable set of values per query name.
type stubProvider struct {
	values map[string]float64
	err    error
}

func (s *stubProvider) Query(_ context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	if s.err != nil {
		return domain.MetricResult{}, s.err
	}
	return domain.MetricResult{Value: s.values[q.Name]}, nil
}

func (s *stubProvider) HealthCheck(_ context.Context) error { return nil }

func ptr(v float64) *float64 { return &v }

func canaryWithChecks(checks []kanaryv1alpha1.MetricCheck, failedChecks int32) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			Strategy: kanaryv1alpha1.Strategy{MaxFailedChecks: 2},
			Analysis: kanaryv1alpha1.AnalysisConfig{
				Enabled: true,
				Metrics: checks,
			},
		},
		Status: kanaryv1alpha1.CanaryStatus{
			FailedChecks: failedChecks,
		},
	}
}

func TestEvaluate_AllPass(t *testing.T) {
	t.Parallel()
	p := &stubProvider{values: map[string]float64{"error_rate": 0.01}}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "error_rate", Query: "rate(errors[1m])", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(0.05)}},
	}, 1)

	res, err := e.Evaluate(context.Background(), c)
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Equal(t, int32(0), res.FailedChecks, "should reset on pass")
	require.Len(t, res.Report.Results, 1)
	require.True(t, res.Report.Results[0].Passed)
}

func TestEvaluate_OneFails(t *testing.T) {
	t.Parallel()
	p := &stubProvider{values: map[string]float64{"error_rate": 0.10}}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "error_rate", Query: "rate(errors[1m])", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(0.05)}},
	}, 0)

	res, err := e.Evaluate(context.Background(), c)
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, int32(1), res.FailedChecks, "should increment on failure")
}

func TestEvaluate_ConsecutiveIncrement(t *testing.T) {
	t.Parallel()
	p := &stubProvider{values: map[string]float64{"latency": 500}}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "latency", Query: "avg(latency)", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(200.0)}},
	}, 2)

	res, err := e.Evaluate(context.Background(), c)
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, int32(3), res.FailedChecks)
}

func TestEvaluate_ThresholdMin(t *testing.T) {
	t.Parallel()
	p := &stubProvider{values: map[string]float64{"success_rate": 0.90}}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "success_rate", Query: "rate(success[1m])", ThresholdRange: kanaryv1alpha1.ThresholdRange{Min: ptr(0.95)}},
	}, 0)

	res, err := e.Evaluate(context.Background(), c)
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.False(t, res.Report.Results[0].Passed)
}

func TestEvaluate_MultipleChecks(t *testing.T) {
	t.Parallel()
	p := &stubProvider{values: map[string]float64{"errors": 0.01, "latency": 150}}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "errors", Query: "errors", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(0.05)}},
		{Name: "latency", Query: "latency", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(200.0)}},
	}, 0)

	res, err := e.Evaluate(context.Background(), c)
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Len(t, res.Report.Results, 2)
}

func TestEvaluate_ProviderError(t *testing.T) {
	t.Parallel()
	p := &stubProvider{err: errors.New("network timeout")}
	e := analysis.New(p)

	c := canaryWithChecks([]kanaryv1alpha1.MetricCheck{
		{Name: "errors", Query: "up", ThresholdRange: kanaryv1alpha1.ThresholdRange{Max: ptr(1.0)}},
	}, 0)

	_, err := e.Evaluate(context.Background(), c)
	require.ErrorContains(t, err, "network timeout")
}

func TestShouldRollback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		failedChecks int32
		maxFailed    int32
		want         bool
	}{
		{"below threshold", 1, 2, false},
		{"at threshold", 2, 2, false},
		{"exceeds threshold", 3, 2, true},
		{"default max (0→2)", 2, 0, false},
		{"default max exceeded", 3, 0, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &kanaryv1alpha1.Canary{
				Spec:   kanaryv1alpha1.CanarySpec{Strategy: kanaryv1alpha1.Strategy{MaxFailedChecks: tc.maxFailed}},
				Status: kanaryv1alpha1.CanaryStatus{FailedChecks: tc.failedChecks},
			}
			require.Equal(t, tc.want, analysis.ShouldRollback(c))
		})
	}
}
