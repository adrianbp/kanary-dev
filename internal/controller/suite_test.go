package controller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"golang.org/x/time/rate"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/controller"
	"github.com/adrianbp/kanary-dev/internal/traffic"
	"github.com/adrianbp/kanary-dev/internal/traffic/nginx"
	"github.com/adrianbp/kanary-dev/internal/workload"
)

var (
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

const testNamespace = "envtest"

func TestControllerSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.Background())

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		Skip("KUBEBUILDER_ASSETS not set — skipping envtest suite")
	}

	sc := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(sc)).To(Succeed())
	Expect(kanaryv1alpha1.AddToScheme(sc)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		Scheme:                sc,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: sc})
	Expect(err).NotTo(HaveOccurred())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 sc,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	Expect(err).NotTo(HaveOccurred())

	// Slow rate limiter: cap at 5 reconciles/sec so envtest doesn't peg the CPU.
	slowRL := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](100*time.Millisecond, 10*time.Second),
		&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: slowBucket()},
	)

	trafficFactory := traffic.NewFactory()
	trafficFactory.Register(kanaryv1alpha1.TrafficProviderNginx, nginx.New(mgr.GetClient()))

	rec := &controller.CanaryReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		Recorder:           record.NewFakeRecorder(100),
		TrafficFactory:     trafficFactory,
		WorkloadReconciler: workload.New(mgr.GetClient(), mgr.GetScheme()),
		ControllerOptions:  ctrlcontroller.Options{RateLimiter: slowRL},
	}
	Expect(rec.SetupWithManager(mgr)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()

	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	_ = k8sClient.Create(ctx, ns)
})

var _ = AfterSuite(func() {
	cancel()
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

// newDeployment builds a minimal Deployment for use in tests.
func newDeployment(name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:stable"}},
				},
			},
		},
	}
}

// newCanary builds a minimal manual Canary CR for use in tests.
func newCanary(name, targetName string, steps []kanaryv1alpha1.Step) *kanaryv1alpha1.Canary {
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Kind: "Deployment", Name: targetName},
			Service:   kanaryv1alpha1.ServiceSpec{Port: 80},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:       kanaryv1alpha1.TrafficProviderNginx,
				IngressRef: &kanaryv1alpha1.LocalObjectReference{Name: targetName},
			},
			Strategy: kanaryv1alpha1.Strategy{
				Mode:  kanaryv1alpha1.StrategyManual,
				Steps: steps,
			},
			// CRD enum validation requires a valid type even when analysis is disabled.
			Analysis: kanaryv1alpha1.AnalysisConfig{
				Enabled:  false,
				Provider: kanaryv1alpha1.MetricProviderConfig{Type: kanaryv1alpha1.MetricProviderPrometheus},
			},
		},
	}
}

// slowBucket returns a token-bucket limiter capped at 5 ops/sec, burst 10.
// Used to throttle the test controller so envtest doesn't saturate the CPU.
func slowBucket() *rate.Limiter { return rate.NewLimiter(rate.Limit(5), 10) }
