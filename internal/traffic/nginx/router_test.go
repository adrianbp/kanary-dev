package nginx_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/traffic/nginx"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sc))
	require.NoError(t, kanaryv1alpha1.AddToScheme(sc))
	require.NoError(t, networkingv1.AddToScheme(sc))
	return sc
}

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		Build()
}

func stableIngress(name, namespace string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: name,
									Port: networkingv1.ServiceBackendPort{Number: 80},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func stableIngressWithDefaultBackend(name, namespace string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: name,
					Port: networkingv1.ServiceBackendPort{Number: 80},
				},
			},
		},
	}
}

func canaryWithIngress(name, namespace string) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Kind: "Deployment", Name: name},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:       kanaryv1alpha1.TrafficProviderNginx,
				IngressRef: &kanaryv1alpha1.LocalObjectReference{Name: name},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Reconcile
// ---------------------------------------------------------------------------

func TestReconcile_CreatesSiblingIngress(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableIngress(name, ns)
	canary := canaryWithIngress(name, ns)
	c := fakeClient(t, stable, canary)
	r := nginx.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 25))

	sibling := &networkingv1.Ingress{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name + "-kanary", Namespace: ns}, sibling))
	require.Equal(t, "true", sibling.Annotations[nginx.AnnotationCanary])
	require.Equal(t, "25", sibling.Annotations[nginx.AnnotationCanaryWeight])
	require.Equal(t, name+"-canary", sibling.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name)
}

func TestReconcile_RewritesDefaultBackend(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableIngressWithDefaultBackend(name, ns)
	canary := canaryWithIngress(name, ns)
	c := fakeClient(t, stable, canary)
	r := nginx.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 25))

	sibling := &networkingv1.Ingress{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name + "-kanary", Namespace: ns}, sibling))
	require.NotNil(t, sibling.Spec.DefaultBackend)
	require.NotNil(t, sibling.Spec.DefaultBackend.Service)
	require.Equal(t, name+"-canary", sibling.Spec.DefaultBackend.Service.Name)
}

func TestReconcile_UpdatesSiblingWeight(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableIngress(name, ns)
	canary := canaryWithIngress(name, ns)
	c := fakeClient(t, stable, canary)
	r := nginx.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 10))
	require.NoError(t, r.Reconcile(context.Background(), canary, 50))

	sibling := &networkingv1.Ingress{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name + "-kanary", Namespace: ns}, sibling))
	require.Equal(t, "50", sibling.Annotations[nginx.AnnotationCanaryWeight])
}

func TestReconcile_IdempotentSameWeight(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableIngress(name, ns)
	canary := canaryWithIngress(name, ns)
	c := fakeClient(t, stable, canary)
	r := nginx.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 30))
	require.NoError(t, r.Reconcile(context.Background(), canary, 30))

	sibling := &networkingv1.Ingress{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name + "-kanary", Namespace: ns}, sibling))
	require.Equal(t, "30", sibling.Annotations[nginx.AnnotationCanaryWeight])
}

func TestReconcile_ErrorWhenIngressRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type: kanaryv1alpha1.TrafficProviderNginx,
				// IngressRef deliberately nil
			},
		},
	}
	r := nginx.New(fakeClient(t))
	err := r.Reconcile(context.Background(), canary, 10)
	require.ErrorIs(t, err, kerr.ErrInvalidSpec)
}

func TestReconcile_ErrorOnWeightOutOfRange(t *testing.T) {
	t.Parallel()
	canary := canaryWithIngress("x", "ns")
	r := nginx.New(fakeClient(t))

	require.ErrorIs(t, r.Reconcile(context.Background(), canary, -1), kerr.ErrInvalidSpec)
	require.ErrorIs(t, r.Reconcile(context.Background(), canary, 101), kerr.ErrInvalidSpec)
}

func TestReconcile_ErrorWhenStableIngressMissing(t *testing.T) {
	t.Parallel()
	canary := canaryWithIngress("missing", "ns")
	r := nginx.New(fakeClient(t)) // no ingress in the fake store

	err := r.Reconcile(context.Background(), canary, 10)
	require.ErrorIs(t, err, kerr.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

func TestReset_DeletesSiblingIngress(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableIngress(name, ns)
	canary := canaryWithIngress(name, ns)
	c := fakeClient(t, stable, canary)
	r := nginx.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 40))
	require.NoError(t, r.Reset(context.Background(), canary))

	sibling := &networkingv1.Ingress{}
	err := c.Get(context.Background(),
		types.NamespacedName{Name: name + "-kanary", Namespace: ns}, sibling)
	require.True(t, apierrors.IsNotFound(err), "sibling should be deleted after reset")
}

func TestReset_IdempotentWhenSiblingMissing(t *testing.T) {
	t.Parallel()
	canary := canaryWithIngress("api", "prod")
	r := nginx.New(fakeClient(t))

	// No sibling exists — should not error.
	require.NoError(t, r.Reset(context.Background(), canary))
}

func TestReset_NoopWhenIngressRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{Type: kanaryv1alpha1.TrafficProviderNginx},
		},
	}
	r := nginx.New(fakeClient(t))
	require.NoError(t, r.Reset(context.Background(), canary))
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestStatus_ReturnsSiblingWeight(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	tests := []struct {
		weight     int32
		wantCanary int32
		wantStable int32
	}{
		{0, 0, 100},
		{10, 10, 90},
		{50, 50, 50},
		{100, 100, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(strconv.Itoa(int(tc.weight))+"%", func(t *testing.T) {
			t.Parallel()
			stable := stableIngress(name, ns)
			canary := canaryWithIngress(name, ns)
			c := fakeClient(t, stable, canary)
			r := nginx.New(c)

			require.NoError(t, r.Reconcile(context.Background(), canary, tc.weight))

			status, err := r.Status(context.Background(), canary)
			require.NoError(t, err)
			require.Equal(t, tc.wantCanary, status.CanaryWeight)
			require.Equal(t, tc.wantStable, status.StableWeight)
			require.False(t, status.ObservedAt.IsZero())
		})
	}
}

func TestStatus_Returns100StableWhenNoSibling(t *testing.T) {
	t.Parallel()
	canary := canaryWithIngress("api", "prod")
	r := nginx.New(fakeClient(t))

	status, err := r.Status(context.Background(), canary)
	require.NoError(t, err)
	require.Equal(t, int32(100), status.StableWeight)
	require.Equal(t, int32(0), status.CanaryWeight)
}

func TestStatus_ErrorWhenIngressRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{Type: kanaryv1alpha1.TrafficProviderNginx},
		},
	}
	r := nginx.New(fakeClient(t))
	_, err := r.Status(context.Background(), canary)
	require.ErrorIs(t, err, kerr.ErrInvalidSpec)
}
