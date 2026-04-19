// Package telemetry registers the operator's own Prometheus metrics (SPEC §9.2).
//
// All metrics are registered against controller-runtime's shared registry so
// they are served on the manager's /metrics endpoint without extra wiring.
package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
)

// allPhases lists every phase value so SetPhase can zero out stale label sets.
var allPhases = []kanaryv1alpha1.Phase{
	kanaryv1alpha1.PhaseIdle,
	kanaryv1alpha1.PhaseProgressing,
	kanaryv1alpha1.PhaseAnalyzing,
	kanaryv1alpha1.PhasePromoting,
	kanaryv1alpha1.PhaseAwaitingPromotion,
	kanaryv1alpha1.PhaseSucceeded,
	kanaryv1alpha1.PhaseRolledBack,
	kanaryv1alpha1.PhaseFailed,
}

var (
	// CanaryPhase exposes the current phase of each Canary as a gauge.
	// Only the active phase label combination is set to 1; all others are 0.
	CanaryPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kanary_canary_phase",
		Help: "Current phase of the Canary (1 = active phase).",
	}, []string{"namespace", "name", "phase"})

	// CanaryStepWeight exposes the current canary traffic weight.
	CanaryStepWeight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kanary_canary_step_weight",
		Help: "Current canary traffic weight (0–100).",
	}, []string{"namespace", "name"})

	// CanaryPromotionsTotal counts completed promotions by result (succeeded|rolled_back).
	CanaryPromotionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kanary_canary_promotions_total",
		Help: "Total canary promotions by result.",
	}, []string{"namespace", "result"})

	// CanaryRollbacksTotal counts rollbacks by cause (abort|analysis_failed).
	CanaryRollbacksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kanary_canary_rollbacks_total",
		Help: "Total canary rollbacks by cause.",
	}, []string{"namespace", "reason"})

	// AnalysisChecksTotal counts individual metric checks by provider and outcome.
	AnalysisChecksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kanary_analysis_checks_total",
		Help: "Total analysis metric checks by provider, metric name and outcome.",
	}, []string{"provider", "metric", "passed"})

	// ReconcileDuration measures the latency of each Reconcile call.
	ReconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kanary_reconcile_duration_seconds",
		Help:    "Reconcile call latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"controller"})

	// ProviderErrorsTotal counts metric-provider errors by provider type and kind.
	ProviderErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kanary_provider_errors_total",
		Help: "Total metric provider errors by provider and kind.",
	}, []string{"provider", "kind"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		CanaryPhase,
		CanaryStepWeight,
		CanaryPromotionsTotal,
		CanaryRollbacksTotal,
		AnalysisChecksTotal,
		ReconcileDuration,
		ProviderErrorsTotal,
	)
}

// SetPhase updates the phase gauge for a single Canary, zeroing all other phases
// so exactly one label combination is 1 at any time.
func SetPhase(namespace, name string, phase kanaryv1alpha1.Phase) {
	for _, p := range allPhases {
		CanaryPhase.WithLabelValues(namespace, name, string(p)).Set(0)
	}
	CanaryPhase.WithLabelValues(namespace, name, string(phase)).Set(1)
}

// DeleteCanary removes all gauge label series for a Canary (call on terminal phases
// after a short delay or on CR deletion to avoid stale series).
func DeleteCanary(namespace, name string) {
	for _, p := range allPhases {
		CanaryPhase.DeleteLabelValues(namespace, name, string(p))
	}
	CanaryStepWeight.DeleteLabelValues(namespace, name)
}

// RecordAnalysisResult increments AnalysisChecksTotal for each result in a report.
func RecordAnalysisResult(provider string, results []kanaryv1alpha1.AnalysisResult) {
	for _, r := range results {
		passed := "false"
		if r.Passed {
			passed = "true"
		}
		AnalysisChecksTotal.WithLabelValues(provider, r.Metric, passed).Inc()
	}
}
