package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/adrianbp/kanary-dev/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sc))
	require.NoError(t, v1alpha1.AddToScheme(sc))
	return sc
}

func minimalCanary(name, namespace, targetName string, steps []v1alpha1.Step) *v1alpha1.Canary {
	return &v1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.CanarySpec{
			TargetRef: v1alpha1.TargetRef{Kind: "Deployment", Name: targetName},
			TrafficProvider: v1alpha1.TrafficProvider{
				Type:       v1alpha1.TrafficProviderNginx,
				IngressRef: &v1alpha1.LocalObjectReference{Name: targetName},
			},
			Strategy: v1alpha1.Strategy{
				Mode:  v1alpha1.StrategyManual,
				Steps: steps,
			},
		},
	}
}

func existingDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

// ---------------------------------------------------------------------------
// Defaulting (#012)
// ---------------------------------------------------------------------------

func TestDefault_SetsStrategyMode(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{Spec: v1alpha1.CanarySpec{}}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, v1alpha1.StrategyManual, c.Spec.Strategy.Mode)
}

func TestDefault_DoesNotOverrideExistingMode(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{Spec: v1alpha1.CanarySpec{
		Strategy: v1alpha1.Strategy{Mode: v1alpha1.StrategyProgressive},
	}}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, v1alpha1.StrategyProgressive, c.Spec.Strategy.Mode)
}

func TestDefault_SetsMaxFailedChecks(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, int32(2), c.Spec.Strategy.MaxFailedChecks)
}

func TestDefault_DoesNotOverrideMaxFailedChecks(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{Spec: v1alpha1.CanarySpec{
		Strategy: v1alpha1.Strategy{MaxFailedChecks: 5},
	}}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, int32(5), c.Spec.Strategy.MaxFailedChecks)
}

func TestDefault_SetsStepInterval(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, 2*time.Minute, c.Spec.Strategy.StepInterval.Duration)
}

func TestDefault_SetsTargetRefDefaults(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := &v1alpha1.Canary{Spec: v1alpha1.CanarySpec{
		TargetRef: v1alpha1.TargetRef{Name: "my-app"},
	}}
	require.NoError(t, wh.Default(context.Background(), c))
	require.Equal(t, "Deployment", c.Spec.TargetRef.Kind)
	require.Equal(t, "apps/v1", c.Spec.TargetRef.APIVersion)
}

// ---------------------------------------------------------------------------
// Step validation (#011)
// ---------------------------------------------------------------------------

func TestValidate_MonotonicSteps_Valid(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 10}, {Weight: 50}, {Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), c)
	require.NoError(t, err)
}

func TestValidate_StepsNotMonotonic(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 50}, {Weight: 10}, {Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
	require.True(t, apierrors.IsInvalid(err))
}

func TestValidate_LastStepNot100(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 10}, {Weight: 50}})
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
	require.True(t, apierrors.IsInvalid(err))
}

func TestValidate_WeightOutOfRange(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 0}, {Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
}

func TestValidate_EqualAdjacentWeights(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 50}, {Weight: 50}, {Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Analysis validation (#011)
// ---------------------------------------------------------------------------

func TestValidate_AnalysisDisabled_NoProviderRequired(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app", []v1alpha1.Step{{Weight: 100}})
	c.Spec.Analysis.Enabled = false
	_, err := wh.ValidateCreate(context.Background(), c)
	require.NoError(t, err)
}

func TestValidate_AnalysisEnabled_MissingProvider(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app", []v1alpha1.Step{{Weight: 100}})
	c.Spec.Analysis.Enabled = true
	c.Spec.Analysis.Metrics = []v1alpha1.MetricCheck{{
		Name: "rps", Query: "rate(http_requests_total[1m])",
		ThresholdRange: v1alpha1.ThresholdRange{},
	}}
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
}

func TestValidate_AnalysisEnabled_MissingMetrics(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app", []v1alpha1.Step{{Weight: 100}})
	c.Spec.Analysis.Enabled = true
	c.Spec.Analysis.Provider.Type = v1alpha1.MetricProviderPrometheus
	_, err := wh.ValidateCreate(context.Background(), c)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// targetRef existence check (#011)
// ---------------------------------------------------------------------------

func TestValidate_TargetRefExists(t *testing.T) {
	t.Parallel()
	sc := newScheme(t)
	deploy := existingDeployment("my-app", "prod")
	c := fake.NewClientBuilder().WithScheme(sc).WithObjects(deploy).Build()
	wh := &v1alpha1.CanaryWebhook{APIReader: c}

	canary := minimalCanary("x", "prod", "my-app", []v1alpha1.Step{{Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), canary)
	require.NoError(t, err)
}

func TestValidate_TargetRefNotFound(t *testing.T) {
	t.Parallel()
	sc := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(sc).Build() // no deployment
	wh := &v1alpha1.CanaryWebhook{APIReader: c}

	canary := minimalCanary("x", "prod", "missing-app", []v1alpha1.Step{{Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), canary)
	require.Error(t, err)
	require.True(t, apierrors.IsInvalid(err))
}

func TestValidate_NilClient_SkipsExistenceCheck(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{APIReader: nil}
	canary := minimalCanary("x", "prod", "any-app", []v1alpha1.Step{{Weight: 100}})
	_, err := wh.ValidateCreate(context.Background(), canary)
	require.NoError(t, err)
}

func TestValidate_Update_CallsValidate(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	old := minimalCanary("x", "ns", "app", []v1alpha1.Step{{Weight: 100}})
	bad := minimalCanary("x", "ns", "app",
		[]v1alpha1.Step{{Weight: 50}, {Weight: 10}, {Weight: 100}}) // not monotonic
	_, err := wh.ValidateUpdate(context.Background(), old, bad)
	require.Error(t, err)
}

func TestValidate_Delete_AlwaysAllowed(t *testing.T) {
	t.Parallel()
	wh := &v1alpha1.CanaryWebhook{}
	c := minimalCanary("x", "ns", "app", []v1alpha1.Step{{Weight: 100}})
	_, err := wh.ValidateDelete(context.Background(), c)
	require.NoError(t, err)
}
