package traffic_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/traffic"
)

// stubRouter satisfies traffic.Router for testing.
type stubRouter struct{}

func (s *stubRouter) Reconcile(_ context.Context, _ *kanaryv1alpha1.Canary, _ int32) error {
	return nil
}
func (s *stubRouter) Reset(_ context.Context, _ *kanaryv1alpha1.Canary) error { return nil }
func (s *stubRouter) Status(_ context.Context, _ *kanaryv1alpha1.Canary) (domain.TrafficStatus, error) {
	return domain.TrafficStatus{}, nil
}

func canaryWithProvider(t kanaryv1alpha1.TrafficProviderType) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{Type: t},
		},
	}
}

func TestFactory_RegisterAndRetrieve(t *testing.T) {
	t.Parallel()
	f := traffic.NewFactory()
	r := &stubRouter{}
	f.Register(kanaryv1alpha1.TrafficProviderNginx, r)

	got, err := f.Router(canaryWithProvider(kanaryv1alpha1.TrafficProviderNginx))
	require.NoError(t, err)
	require.Equal(t, r, got)
}

func TestFactory_UnregisteredProvider(t *testing.T) {
	t.Parallel()
	f := traffic.NewFactory()

	_, err := f.Router(canaryWithProvider(kanaryv1alpha1.TrafficProviderNginx))
	require.ErrorIs(t, err, kerr.ErrUnsupportedProvider)
}

func TestFactory_RegisterReplaces(t *testing.T) {
	t.Parallel()
	f := traffic.NewFactory()
	first := &stubRouter{}
	second := &stubRouter{}

	f.Register(kanaryv1alpha1.TrafficProviderNginx, first)
	f.Register(kanaryv1alpha1.TrafficProviderNginx, second)

	got, err := f.Router(canaryWithProvider(kanaryv1alpha1.TrafficProviderNginx))
	require.NoError(t, err)
	require.Equal(t, second, got)
}

func TestFactory_MultipleProviders(t *testing.T) {
	t.Parallel()
	f := traffic.NewFactory()
	nginx := &stubRouter{}
	ocp := &stubRouter{}

	f.Register(kanaryv1alpha1.TrafficProviderNginx, nginx)
	f.Register(kanaryv1alpha1.TrafficProviderOpenShiftRoute, ocp)

	gotNginx, err := f.Router(canaryWithProvider(kanaryv1alpha1.TrafficProviderNginx))
	require.NoError(t, err)
	require.Equal(t, nginx, gotNginx)

	gotOcp, err := f.Router(canaryWithProvider(kanaryv1alpha1.TrafficProviderOpenShiftRoute))
	require.NoError(t, err)
	require.Equal(t, ocp, gotOcp)
}
