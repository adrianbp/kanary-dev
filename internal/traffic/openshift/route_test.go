package openshift_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/traffic/openshift"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	require.NoError(t, kanaryv1alpha1.AddToScheme(sc))
	return sc
}

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		Build()
}

// stableRoute returns a minimal OpenShift Route as unstructured, with
// spec.to.weight=256 pointing at the stable Service.
func stableRoute(name, namespace, stableSvc string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(openshift.RouteGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedField(u.Object, "Service", "spec", "to", "kind")
	_ = unstructured.SetNestedField(u.Object, stableSvc, "spec", "to", "name")
	_ = unstructured.SetNestedField(u.Object, int64(256), "spec", "to", "weight")
	return u
}

func canaryWithRoute(name, namespace string) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Kind: "Deployment", Name: name},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:     kanaryv1alpha1.TrafficProviderOpenShiftRoute,
				RouteRef: &kanaryv1alpha1.LocalObjectReference{Name: name},
			},
		},
	}
}

func fakeClientWithRoute(t *testing.T, name, namespace string) client.Client {
	t.Helper()
	route := stableRoute(name, namespace, name)
	sc := runtime.NewScheme()
	require.NoError(t, kanaryv1alpha1.AddToScheme(sc))
	return fake.NewClientBuilder().
		WithScheme(sc).
		WithObjects(route).
		Build()
}

// ---------------------------------------------------------------------------
// Reconcile — weight splitting
// ---------------------------------------------------------------------------

func TestReconcile_SetsAlternateBackends(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	c := fakeClientWithRoute(t, name, ns)
	canary := canaryWithRoute(name, ns)
	r := openshift.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 25))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(openshift.RouteGVK)
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: name, Namespace: ns}, got))

	alts, found, err := unstructured.NestedSlice(got.Object, "spec", "alternateBackends")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, alts, 1)

	alt := alts[0].(map[string]interface{})
	require.Equal(t, name+"-canary", alt["name"])
	require.Equal(t, int64(64), alt["weight"]) // 25*256/100 = 64

	stableW, _, _ := unstructured.NestedInt64(got.Object, "spec", "to", "weight")
	require.Equal(t, int64(192), stableW) // 256-64
}

func TestReconcile_ZeroWeight_NoCanaryTraffic(t *testing.T) {
	t.Parallel()
	c := fakeClientWithRoute(t, "api", "prod")
	canary := canaryWithRoute("api", "prod")
	r := openshift.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 0))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(openshift.RouteGVK)
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: "api", Namespace: "prod"}, got))

	alts, _, _ := unstructured.NestedSlice(got.Object, "spec", "alternateBackends")
	canaryW := alts[0].(map[string]interface{})["weight"]
	require.Equal(t, int64(0), canaryW)
	stableW, _, _ := unstructured.NestedInt64(got.Object, "spec", "to", "weight")
	require.Equal(t, int64(256), stableW)
}

func TestReconcile_100Pct_AllCanary(t *testing.T) {
	t.Parallel()
	c := fakeClientWithRoute(t, "api", "prod")
	canary := canaryWithRoute("api", "prod")
	r := openshift.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 100))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(openshift.RouteGVK)
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: "api", Namespace: "prod"}, got))

	alts, _, _ := unstructured.NestedSlice(got.Object, "spec", "alternateBackends")
	canaryW := alts[0].(map[string]interface{})["weight"]
	require.Equal(t, int64(256), canaryW)
	stableW, _, _ := unstructured.NestedInt64(got.Object, "spec", "to", "weight")
	require.Equal(t, int64(0), stableW)
}

func TestReconcile_Idempotent(t *testing.T) {
	t.Parallel()
	c := fakeClientWithRoute(t, "api", "prod")
	canary := canaryWithRoute("api", "prod")
	r := openshift.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 50))
	require.NoError(t, r.Reconcile(context.Background(), canary, 50))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(openshift.RouteGVK)
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: "api", Namespace: "prod"}, got))

	alts, _, _ := unstructured.NestedSlice(got.Object, "spec", "alternateBackends")
	require.Len(t, alts, 1)
}

func TestReconcile_ErrorWhenRouteRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type: kanaryv1alpha1.TrafficProviderOpenShiftRoute,
			},
		},
	}
	r := openshift.New(fakeClient(t))
	require.ErrorIs(t, r.Reconcile(context.Background(), canary, 10), kerr.ErrInvalidSpec)
}

func TestReconcile_ErrorWeightOutOfRange(t *testing.T) {
	t.Parallel()
	canary := canaryWithRoute("x", "ns")
	r := openshift.New(fakeClient(t))
	require.ErrorIs(t, r.Reconcile(context.Background(), canary, -1), kerr.ErrInvalidSpec)
	require.ErrorIs(t, r.Reconcile(context.Background(), canary, 101), kerr.ErrInvalidSpec)
}

func TestReconcile_ErrorWhenRouteMissing(t *testing.T) {
	t.Parallel()
	canary := canaryWithRoute("missing", "ns")
	r := openshift.New(fakeClient(t))
	require.ErrorIs(t, r.Reconcile(context.Background(), canary, 10), kerr.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

func TestReset_RemovesAlternateBackends(t *testing.T) {
	t.Parallel()
	c := fakeClientWithRoute(t, "api", "prod")
	canary := canaryWithRoute("api", "prod")
	r := openshift.New(c)

	require.NoError(t, r.Reconcile(context.Background(), canary, 40))
	require.NoError(t, r.Reset(context.Background(), canary))

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(openshift.RouteGVK)
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: "api", Namespace: "prod"}, got))

	_, found, _ := unstructured.NestedSlice(got.Object, "spec", "alternateBackends")
	require.False(t, found, "alternateBackends should be absent after reset")

	stableW, _, _ := unstructured.NestedInt64(got.Object, "spec", "to", "weight")
	require.Equal(t, int64(256), stableW)
}

func TestReset_NoopWhenRouteRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type: kanaryv1alpha1.TrafficProviderOpenShiftRoute,
			},
		},
	}
	r := openshift.New(fakeClient(t))
	require.NoError(t, r.Reset(context.Background(), canary))
}

func TestReset_NoopWhenRouteMissing(t *testing.T) {
	t.Parallel()
	canary := canaryWithRoute("gone", "ns")
	r := openshift.New(fakeClient(t))
	require.NoError(t, r.Reset(context.Background(), canary))
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestStatus_ReturnsObservedWeights(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pct        int32
		wantCanary int32
		wantStable int32
	}{
		{0, 0, 100},
		{25, 25, 75},  // 64/256 = 25%
		{50, 50, 50},  // 128/256 = 50%
		{100, 100, 0}, // 256/256 = 100%
	}
	for _, tc := range tests {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			c := fakeClientWithRoute(t, "api", "prod")
			canary := canaryWithRoute("api", "prod")
			r := openshift.New(c)

			require.NoError(t, r.Reconcile(context.Background(), canary, tc.pct))

			status, err := r.Status(context.Background(), canary)
			require.NoError(t, err)
			require.Equal(t, tc.wantCanary, status.CanaryWeight)
			require.Equal(t, tc.wantStable, status.StableWeight)
			require.False(t, status.ObservedAt.IsZero())
		})
	}
}

func TestStatus_Returns100StableWhenNoAlternateBackend(t *testing.T) {
	t.Parallel()
	c := fakeClientWithRoute(t, "api", "prod")
	canary := canaryWithRoute("api", "prod")
	r := openshift.New(c)

	status, err := r.Status(context.Background(), canary)
	require.NoError(t, err)
	require.Equal(t, int32(100), status.StableWeight)
	require.Equal(t, int32(0), status.CanaryWeight)
}

func TestStatus_Returns100StableWhenRouteMissing(t *testing.T) {
	t.Parallel()
	canary := canaryWithRoute("gone", "ns")
	r := openshift.New(fakeClient(t))

	status, err := r.Status(context.Background(), canary)
	require.NoError(t, err)
	require.Equal(t, int32(100), status.StableWeight)
}

func TestStatus_ErrorWhenRouteRefNil(t *testing.T) {
	t.Parallel()
	canary := &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: kanaryv1alpha1.CanarySpec{
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type: kanaryv1alpha1.TrafficProviderOpenShiftRoute,
			},
		},
	}
	r := openshift.New(fakeClient(t))
	_, err := r.Status(context.Background(), canary)
	require.ErrorIs(t, err, kerr.ErrInvalidSpec)
}
