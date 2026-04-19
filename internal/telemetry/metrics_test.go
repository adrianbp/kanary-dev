package telemetry_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/telemetry"
)

func gaugeValue(t *testing.T, g interface {
	WithLabelValues(...string) interface{ Write(*dto.Metric) error }
}, lvs ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, g.WithLabelValues(lvs...).Write(m))
	return m.GetGauge().GetValue()
}

func TestSetPhase_SetsActivePhaseToOne(t *testing.T) {
	t.Parallel()
	telemetry.SetPhase("ns", "app", kanaryv1alpha1.PhaseProgressing)

	m := &dto.Metric{}
	require.NoError(t, telemetry.CanaryPhase.WithLabelValues("ns", "app", "Progressing").Write(m))
	require.Equal(t, float64(1), m.GetGauge().GetValue())
}

func TestSetPhase_ZerosOtherPhases(t *testing.T) {
	t.Parallel()
	telemetry.SetPhase("ns2", "app2", kanaryv1alpha1.PhaseSucceeded)
	telemetry.SetPhase("ns2", "app2", kanaryv1alpha1.PhaseProgressing)

	m := &dto.Metric{}
	require.NoError(t, telemetry.CanaryPhase.WithLabelValues("ns2", "app2", "Succeeded").Write(m))
	require.Equal(t, float64(0), m.GetGauge().GetValue(), "Succeeded should be zeroed after phase change")

	require.NoError(t, telemetry.CanaryPhase.WithLabelValues("ns2", "app2", "Progressing").Write(m))
	require.Equal(t, float64(1), m.GetGauge().GetValue())
}

func TestRecordAnalysisResult_Increments(t *testing.T) {
	t.Parallel()
	results := []kanaryv1alpha1.AnalysisResult{
		{Metric: "error_rate", Passed: true},
		{Metric: "latency", Passed: false},
	}
	telemetry.RecordAnalysisResult("prometheus", results)

	mPass := &dto.Metric{}
	require.NoError(t, telemetry.AnalysisChecksTotal.WithLabelValues("prometheus", "error_rate", "true").Write(mPass))
	require.Greater(t, mPass.GetCounter().GetValue(), float64(0))

	mFail := &dto.Metric{}
	require.NoError(t, telemetry.AnalysisChecksTotal.WithLabelValues("prometheus", "latency", "false").Write(mFail))
	require.Greater(t, mFail.GetCounter().GetValue(), float64(0))
}

func TestDeleteCanary_RemovesSeries(t *testing.T) {
	t.Parallel()
	telemetry.SetPhase("del-ns", "del-app", kanaryv1alpha1.PhaseIdle)
	telemetry.CanaryStepWeight.WithLabelValues("del-ns", "del-app").Set(50)

	telemetry.DeleteCanary("del-ns", "del-app")

	// After delete, writing creates a fresh zero-value series — no panic.
	m := &dto.Metric{}
	require.NoError(t, telemetry.CanaryPhase.WithLabelValues("del-ns", "del-app", "Idle").Write(m))
	require.Equal(t, float64(0), m.GetGauge().GetValue())
}
