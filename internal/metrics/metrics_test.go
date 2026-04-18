package metrics_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/metrics"
)

// stubProvider satisfies metrics.Provider for testing.
type stubProvider struct{}

func (s *stubProvider) Query(_ context.Context, _ domain.MetricQuery) (domain.MetricResult, error) {
	return domain.MetricResult{}, nil
}
func (s *stubProvider) HealthCheck(_ context.Context) error { return nil }

func canaryWithMetricProvider(t kanaryv1alpha1.MetricProviderType) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			Analysis: kanaryv1alpha1.AnalysisConfig{
				Provider: kanaryv1alpha1.MetricProviderConfig{Type: t},
			},
		},
	}
}

func TestMetricsFactory_RegisterAndRetrieve(t *testing.T) {
	t.Parallel()
	f := metrics.NewFactory()
	p := &stubProvider{}
	f.Register(kanaryv1alpha1.MetricProviderPrometheus, p)

	got, err := f.Provider(canaryWithMetricProvider(kanaryv1alpha1.MetricProviderPrometheus))
	require.NoError(t, err)
	require.Equal(t, p, got)
}

func TestMetricsFactory_UnregisteredProvider(t *testing.T) {
	t.Parallel()
	f := metrics.NewFactory()

	_, err := f.Provider(canaryWithMetricProvider(kanaryv1alpha1.MetricProviderPrometheus))
	require.ErrorIs(t, err, kerr.ErrUnsupportedProvider)
}

func TestMetricsFactory_RegisterReplaces(t *testing.T) {
	t.Parallel()
	f := metrics.NewFactory()
	first := &stubProvider{}
	second := &stubProvider{}

	f.Register(kanaryv1alpha1.MetricProviderPrometheus, first)
	f.Register(kanaryv1alpha1.MetricProviderPrometheus, second)

	got, err := f.Provider(canaryWithMetricProvider(kanaryv1alpha1.MetricProviderPrometheus))
	require.NoError(t, err)
	require.Equal(t, second, got)
}

func TestMetricsFactory_MultipleProviders(t *testing.T) {
	t.Parallel()
	f := metrics.NewFactory()
	prom := &stubProvider{}
	dd := &stubProvider{}

	f.Register(kanaryv1alpha1.MetricProviderPrometheus, prom)
	f.Register(kanaryv1alpha1.MetricProviderDatadog, dd)

	gotProm, err := f.Provider(canaryWithMetricProvider(kanaryv1alpha1.MetricProviderPrometheus))
	require.NoError(t, err)
	require.Equal(t, prom, gotProm)

	gotDD, err := f.Provider(canaryWithMetricProvider(kanaryv1alpha1.MetricProviderDatadog))
	require.NoError(t, err)
	require.Equal(t, dd, gotDD)
}
