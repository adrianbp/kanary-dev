package controller_test

import (
	"fmt"
	"math/rand"
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
	timeout  = 15 * time.Second
	interval = 250 * time.Millisecond
)

func randSuffix() string { return fmt.Sprintf("%05d", rand.Intn(99999)) } //nolint:gosec

var _ = Describe("Canary controller", func() {

	Describe("happy path — manual mode", func() {
		var (
			deployName string
			canaryName string
			steps      = []kanaryv1alpha1.Step{{Weight: 10}, {Weight: 50}, {Weight: 100}}
		)

		BeforeEach(func() {
			deployName = "svc-" + randSuffix()
			canaryName = deployName

			// Create Deployment with an explicit revision annotation so
			// deploymentRevision() returns a stable, predictable value ("1").
			deploy := newDeploymentWithRevision(deployName, "1")
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
			Expect(k8sClient.Create(ctx, newStableIngress(deployName))).To(Succeed())
			Expect(k8sClient.Create(ctx, newCanary(canaryName, deployName, steps))).To(Succeed())
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

		It("seeds StableRevision on first reconcile", func() {
			key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}
			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("moves to AwaitingPromotion when a new revision is detected", func() {
			key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

			// Wait for seed pass to complete.
			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Simulate a new Deployment revision ("2") and trigger an immediate
			// reconcile by adding a Canary annotation (AnnotationChangedPredicate).
			patchDeploymentRevision(deployName, "2")
			touchCanary(key)

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAwaitingPromotion))
				g.Expect(c.Status.CurrentWeight).To(Equal(int32(10)))
			}, timeout, interval).Should(Succeed())
		})

		It("advances step on promote annotation", func() {
			key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

			// Seed then start canary.
			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
			patchDeploymentRevision(deployName, "2")
			touchCanary(key)

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAwaitingPromotion))
			}, timeout, interval).Should(Succeed())

			// Now promote.
			c := &kanaryv1alpha1.Canary{}
			Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			patch := client.MergeFrom(c.DeepCopy())
			c.Annotations[kanaryv1alpha1.AnnotationPromote] = "true"
			Expect(k8sClient.Patch(ctx, c, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.CurrentStepIndex).To(Equal(int32(1)))
				g.Expect(c.Annotations).NotTo(HaveKey(kanaryv1alpha1.AnnotationPromote))
			}, timeout, interval).Should(Succeed())
		})

		It("rolls back on abort annotation", func() {
			key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
			patchDeploymentRevision(deployName, "2")
			touchCanary(key)

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAwaitingPromotion))
			}, timeout, interval).Should(Succeed())

			c := &kanaryv1alpha1.Canary{}
			Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			patch := client.MergeFrom(c.DeepCopy())
			c.Annotations[kanaryv1alpha1.AnnotationAbort] = "true"
			Expect(k8sClient.Patch(ctx, c, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseRolledBack))
				g.Expect(c.Annotations).NotTo(HaveKey(kanaryv1alpha1.AnnotationAbort))
			}, timeout, interval).Should(Succeed())
		})

		It("pauses reconciliation when paused annotation is set", func() {
			key := types.NamespacedName{Name: canaryName, Namespace: testNamespace}

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.StableRevision).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
			patchDeploymentRevision(deployName, "2")
			touchCanary(key)

			Eventually(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAwaitingPromotion))
			}, timeout, interval).Should(Succeed())

			// Pause + promote: phase must NOT change.
			c := &kanaryv1alpha1.Canary{}
			Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
			patch := client.MergeFrom(c.DeepCopy())
			c.Annotations[kanaryv1alpha1.AnnotationPaused] = "true"
			c.Annotations[kanaryv1alpha1.AnnotationPromote] = "true"
			Expect(k8sClient.Patch(ctx, c, patch)).To(Succeed())

			Consistently(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.PhaseAwaitingPromotion))
			}, 2*time.Second, interval).Should(Succeed())
		})

		It("skips reconciliation when canary-enabled=false", func() {
			disabledName := "disabled-" + randSuffix()
			dc := newCanary(disabledName, deployName, steps)
			dc.Annotations = map[string]string{
				kanaryv1alpha1.AnnotationCanaryEnabled: "false",
			}
			Expect(k8sClient.Create(ctx, dc)).To(Succeed())

			key := types.NamespacedName{Name: disabledName, Namespace: testNamespace}
			Consistently(func(g Gomega) {
				c := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, c)).To(Succeed())
				g.Expect(c.Status.Phase).To(Equal(kanaryv1alpha1.Phase("")))
			}, 2*time.Second, interval).Should(Succeed())

			_ = k8sClient.Delete(ctx, dc)
		})
	})

	Describe("missing target Deployment", func() {
		It("does not crash when targetRef not found", func() {
			c := newCanary("orphan-"+randSuffix(), "nonexistent-deploy",
				[]kanaryv1alpha1.Step{{Weight: 10}})
			Expect(k8sClient.Create(ctx, c)).To(Succeed())

			key := types.NamespacedName{Name: c.Name, Namespace: testNamespace}
			Consistently(func(g Gomega) {
				got := &kanaryv1alpha1.Canary{}
				g.Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
				g.Expect(got.Status.Phase).NotTo(Equal(kanaryv1alpha1.PhaseFailed))
			}, 2*time.Second, interval).Should(Succeed())

			_ = k8sClient.Delete(ctx, c)
		})
	})
})

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newDeploymentWithRevision(name, revision string) *appsv1.Deployment {
	d := newDeployment(name)
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations["deployment.kubernetes.io/revision"] = revision
	return d
}

// patchDeploymentRevision updates the Deployment's revision annotation to
// simulate what the ReplicaSet controller would do in a real cluster.
func patchDeploymentRevision(name, revision string) {
	GinkgoHelper()
	key := types.NamespacedName{Name: name, Namespace: testNamespace}
	deploy := &appsv1.Deployment{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, deploy)).To(Succeed())
	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Annotations == nil {
		deploy.Annotations = map[string]string{}
	}
	deploy.Annotations["deployment.kubernetes.io/revision"] = revision
	ExpectWithOffset(1, k8sClient.Patch(ctx, deploy, patch)).To(Succeed())
}

// touchCanary adds a dummy annotation to force an immediate reconcile via
// AnnotationChangedPredicate — needed after updating the Deployment since the
// controller does not watch user-owned Deployments directly.
func touchCanary(key types.NamespacedName) {
	GinkgoHelper()
	c := &kanaryv1alpha1.Canary{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, c)).To(Succeed())
	patch := client.MergeFrom(c.DeepCopy())
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations["kanary.io/touched"] = fmt.Sprintf("%d", time.Now().UnixNano())
	ExpectWithOffset(1, k8sClient.Patch(ctx, c, patch)).To(Succeed())
}

func newStableIngress(name string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
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
