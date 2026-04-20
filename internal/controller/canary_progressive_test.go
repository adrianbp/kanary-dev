package controller_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
)

const (
	progressiveTimeout  = 60 * time.Second
	progressiveInterval = 2 * time.Second
)

// newProgressiveCanary builds a Canary in Progressive mode with analysis enabled.
// The fake metric check requires success-rate >= 0.9; tests control the returned
// value via testAnalysisProvider.set().
func newProgressiveCanary(name, targetName string, steps []kanaryv1alpha1.Step, maxFailed int32) *kanaryv1alpha1.Canary {
	minVal := 0.9
	return &kanaryv1alpha1.Canary{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   testNamespace,
			Annotations: map[string]string{},
		},
		Spec: kanaryv1alpha1.CanarySpec{
			TargetRef: kanaryv1alpha1.TargetRef{Kind: "Deployment", Name: targetName},
			Service:   kanaryv1alpha1.ServiceSpec{Port: 80},
			TrafficProvider: kanaryv1alpha1.TrafficProvider{
				Type:       kanaryv1alpha1.TrafficProviderNginx,
				IngressRef: &kanaryv1alpha1.LocalObjectReference{Name: targetName},
			},
			Strategy: kanaryv1alpha1.Strategy{
				Mode:            kanaryv1alpha1.StrategyProgressive,
				Steps:           steps,
				MaxFailedChecks: maxFailed,
			},
			Analysis: kanaryv1alpha1.AnalysisConfig{
				Enabled:  true,
				Provider: kanaryv1alpha1.MetricProviderConfig{Type: kanaryv1alpha1.MetricProviderPrometheus},
				Metrics: []kanaryv1alpha1.MetricCheck{{
					Name:           "success-rate",
					Query:          `rate(requests_total[1m])`,
					ThresholdRange: kanaryv1alpha1.ThresholdRange{Min: &minVal},
				}},
			},
		},
	}
}

var _ = Describe("Canary controller — Progressive mode", func() {
	var (
		deployName string
		canaryName string
	)

	BeforeEach(func() {
		deployName = "pg-" + randSuffix()
		canaryName = deployName

		// Default: analysis passes (value 1.0 >= min 0.9).
		testAnalysisProvider.set(1.0, nil)

		deploy := newDeploymentWithRevision(deployName, "1")
		Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		Expect(k8sClient.Create(ctx, newStableIngress(deployName))).To(Succeed())
	})

	AfterEach(func() {
		bg := metav1.DeletePropagationBackground
		_ = k8sClient.DeleteAllOf(ctx, &kanaryv1alpha1.Canary{},
			client.InNamespace(testNamespace),
			&client.DeleteAllOfOptions{DeleteOptions: client.DeleteOptions{PropagationPolicy: &bg}})
		_ = k8sClient.DeleteAllOf(ctx, &networkingv1.Ingress{}, client.InNamespace(testNamespace))
		_ = k8sClient.DeleteAllOf(ctx, &appsv1.Deployment{},
			client.InNamespace(testNamespace),
			&client.DeleteAllOfOptions{DeleteOptions: client.DeleteOptions{PropagationPolicy: &bg}})

		Eventually(func(g Gomega) {
			list := &kanaryv1alpha1.CanaryList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(testNamespace))).To(Succeed())
			g.Expect(list.Items).To(BeEmpty())
		}, timeout, interval).Should(Succeed())
	})

	// waitForSeed blocks until the canary has a StableRevision.
	waitForSeed := func(key types.NamespacedName) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
		}, timeout, interval).Should(Succeed())
	}

	// triggerNewRevision simulates a new Deployment rollout and forces an
	// immediate reconcile via AnnotationChangedPredicate.
	triggerNewRevision := func(key types.NamespacedName) {
		GinkgoHelper()
		patchDeploymentRevision(deployName, "2")
		touchCanary(key)
	}

	It("enters PhaseAnalyzing when a new revision is detected", func() {
		steps := []kanaryv1alpha1.Step{{Weight: 10}, {Weight: 100}}
		Expect(k8sClient.Create(ctx, newProgressiveCanary(canaryName, deployName, steps, 2))).To(Succeed())
		key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

		waitForSeed(key)
		triggerNewRevision(key)

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAnalyzing))
			g.Expect(c.Status.CurrentWeight).To(Equal(int32(10)))
		}, timeout, interval).Should(Succeed())
	})

	It("auto-promotes when analysis passes (single step)", func() {
		steps := []kanaryv1alpha1.Step{{Weight: 100}}
		Expect(k8sClient.Create(ctx, newProgressiveCanary(canaryName, deployName, steps, 2))).To(Succeed())
		key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

		waitForSeed(key)
		triggerNewRevision(key)

		// Wait for first reconcile to set Analyzing.
		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAnalyzing))
		}, timeout, interval).Should(Succeed())

		// Trigger analysis pass (fakeProvider returns 1.0 → passes threshold 0.9).
		touchCanary(key)

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseSucceeded))
		}, timeout, interval).Should(Succeed())
	})

	It("increments FailedChecks and stays Analyzing on metric failure", func() {
		testAnalysisProvider.set(0.5, nil) // below min 0.9 → fail

		steps := []kanaryv1alpha1.Step{{Weight: 100}}
		Expect(k8sClient.Create(ctx, newProgressiveCanary(canaryName, deployName, steps, 5))).To(Succeed())
		key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

		waitForSeed(key)
		triggerNewRevision(key)

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAnalyzing))
		}, timeout, interval).Should(Succeed())

		// Trigger one analysis pass that fails.
		touchCanary(key)

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAnalyzing))
			g.Expect(c.Status.FailedChecks).To(BeNumerically(">", 0))
		}, timeout, interval).Should(Succeed())
	})

	It("auto-rollback when consecutive failures exceed maxFailedChecks", func() {
		testAnalysisProvider.set(0.0, nil) // always fails

		steps := []kanaryv1alpha1.Step{{Weight: 100}}
		// maxFailedChecks=1: rollback triggers when status.FailedChecks > 1 (i.e. =2)
		// which requires 3 analysis passes after the initial Analyzing entry.
		Expect(k8sClient.Create(ctx, newProgressiveCanary(canaryName, deployName, steps, 1))).To(Succeed())
		key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

		waitForSeed(key)
		triggerNewRevision(key)

		// Drive the analysis loop: touchCanary on each poll to keep triggering
		// reconciles until the rollback threshold is crossed.
		Eventually(func(g Gomega) {
			touchCanary(key)
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseRolledBack))
		}, progressiveTimeout, progressiveInterval).Should(Succeed())
	})

	It("manual promote annotation bypasses analysis and promotes immediately", func() {
		testAnalysisProvider.set(0.0, nil) // would fail if analysis ran

		steps := []kanaryv1alpha1.Step{{Weight: 100}}
		Expect(k8sClient.Create(ctx, newProgressiveCanary(canaryName, deployName, steps, 2))).To(Succeed())
		key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

		waitForSeed(key)
		triggerNewRevision(key)

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAnalyzing))
		}, timeout, interval).Should(Succeed())

		// Apply promote annotation — should skip analysis entirely.
		c := &kanaryv1alpha1.Canary{}
		Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
		patch := client.MergeFrom(c.DeepCopy())
		c.Annotations[kanaryv1alpha1.AnnotationPromote] = annotationTrue
		Expect(k8sClient.Patch(ctx, c, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			c := &kanaryv1alpha1.Canary{}
			g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseSucceeded))
			g.Expect(c.Annotations).NotTo(HaveKey(kanaryv1alpha1.AnnotationPromote))
		}, timeout, interval).Should(Succeed())
	})
})
