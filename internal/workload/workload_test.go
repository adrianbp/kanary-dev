package workload_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/workload"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sc))
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

func stableDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:stable"}}},
			},
		},
	}
}

func canaryResource(name, namespace string, port int32) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Kind: "Deployment", Name: name},
			Service:   kanaryv1alpha1.ServiceSpec{Port: port, TargetPort: "8080"},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type: kanaryv1alpha1.TrafficProviderNginx,
			},
			Strategy: kanaryv1alpha1.Strategy{
				Steps: []kanaryv1alpha1.Step{{Weight: 50}},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// EnsureCanaryDeployment
// ---------------------------------------------------------------------------

func TestEnsureCanaryDeployment_Creates(t *testing.T) {
	t.Parallel()
	const (
		name = "api"
		ns   = "prod"
	)
	stable := stableDeployment(name, ns)
	canary := canaryResource(name, ns, 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))

	got := &appsv1.Deployment{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name + "-canary", Namespace: ns}, got))

	require.Equal(t, int32(1), *got.Spec.Replicas)
	require.Equal(t, "canary", got.Spec.Template.Labels[kanaryv1alpha1.LabelRevision])
	require.Equal(t, "canary", got.Spec.Selector.MatchLabels[kanaryv1alpha1.LabelRevision])
	require.Equal(t, "true", got.Labels[kanaryv1alpha1.LabelManaged])
	require.Equal(t, name, got.Labels[kanaryv1alpha1.LabelCanary])
}

func TestEnsureCanaryDeployment_Idempotent(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))
	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))

	got := &appsv1.Deployment{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "api-canary", Namespace: "prod"}, got))
	require.Equal(t, int32(1), *got.Spec.Replicas)
}

func TestEnsureCanaryDeployment_UpdatesImage(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))

	// Simulate stable image update.
	stable.Spec.Template.Spec.Containers[0].Image = "nginx:canary"
	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))

	got := &appsv1.Deployment{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "api-canary", Namespace: "prod"}, got))
	require.Equal(t, "nginx:canary", got.Spec.Template.Spec.Containers[0].Image)
}

func TestEnsureCanaryDeployment_DoesNotMutateStableSelector(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))

	// The original stable Deployment's selector must not be modified.
	_, hasRevision := stable.Spec.Selector.MatchLabels[kanaryv1alpha1.LabelRevision]
	require.False(t, hasRevision, "stable Deployment selector must not be mutated")
}

// ---------------------------------------------------------------------------
// EnsureServices
// ---------------------------------------------------------------------------

func TestEnsureServices_CreatesStableAndCanary(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))

	stableSvc := &corev1.Service{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "api", Namespace: "prod"}, stableSvc))
	require.Equal(t, int32(80), stableSvc.Spec.Ports[0].Port)
	require.Equal(t, "api", stableSvc.Spec.Selector["app"])
	_, hasRevision := stableSvc.Spec.Selector[kanaryv1alpha1.LabelRevision]
	require.False(t, hasRevision, "stable service selector must not include revision label")

	canarySvc := &corev1.Service{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "api-canary", Namespace: "prod"}, canarySvc))
	require.Equal(t, int32(80), canarySvc.Spec.Ports[0].Port)
	require.Equal(t, "canary", canarySvc.Spec.Selector[kanaryv1alpha1.LabelRevision])
}

func TestEnsureServices_Idempotent(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))
	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))
}

func TestEnsureServices_SkipsWhenPortZero(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 0) // no port
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))

	svc := &corev1.Service{}
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "api", Namespace: "prod"}, svc)
	require.True(t, apierrors.IsNotFound(err), "no Service should be created when port=0")
}

func TestEnsureServices_DoesNotTakeOverUserManagedStableService(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)

	// Pre-existing user stable Service without our managed label.
	userSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []corev1.ServicePort{{Port: 9090}},
		},
	}
	c := fakeClient(t, stable, canary, userSvc)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))

	got := &corev1.Service{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "api", Namespace: "prod"}, got))
	// Port must remain as the user set it.
	require.Equal(t, int32(9090), got.Spec.Ports[0].Port)
}

// ---------------------------------------------------------------------------
// CleanupCanary
// ---------------------------------------------------------------------------

func TestCleanupCanary_RemovesDeploymentAndService(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureCanaryDeployment(context.Background(), canary, stable))
	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))
	require.NoError(t, r.CleanupCanary(context.Background(), canary))

	dep := &appsv1.Deployment{}
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "api-canary", Namespace: "prod"}, dep)
	require.True(t, apierrors.IsNotFound(err), "canary Deployment should be deleted")

	svc := &corev1.Service{}
	err = c.Get(context.Background(),
		types.NamespacedName{Name: "api-canary", Namespace: "prod"}, svc)
	require.True(t, apierrors.IsNotFound(err), "canary Service should be deleted")
}

func TestCleanupCanary_LeavesStableService(t *testing.T) {
	t.Parallel()
	stable := stableDeployment("api", "prod")
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, stable, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.EnsureServices(context.Background(), canary, stable))
	require.NoError(t, r.CleanupCanary(context.Background(), canary))

	stableSvc := &corev1.Service{}
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "api", Namespace: "prod"}, stableSvc)
	require.NoError(t, err, "stable Service should remain after cleanup")
}

func TestCleanupCanary_IdempotentWhenAlreadyGone(t *testing.T) {
	t.Parallel()
	canary := canaryResource("api", "prod", 80)
	c := fakeClient(t, canary)
	r := workload.New(c, newScheme(t))

	require.NoError(t, r.CleanupCanary(context.Background(), canary))
}
