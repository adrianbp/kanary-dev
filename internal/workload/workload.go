// Package workload manages the canary Deployment and Services owned by a Canary.
//
// Design (#014, #015):
//
//   - Canary Deployment: copy of the stable Deployment, 1 replica, pod-template
//     label kanary.io/revision=canary added, owned by the Canary CR.
//   - Canary Service: selects canary pods via the original matchLabels plus
//     kanary.io/revision=canary; owned by the Canary CR.
//   - Stable Service: selects stable pods via the original matchLabels; only
//     created if no Service with that name already exists (we do not take over
//     user-managed Services).
//
// All operations are idempotent.
package workload

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

const revisionCanary = "canary"

// Reconciler creates and maintains the canary workload objects.
type Reconciler struct {
	c      client.Client
	scheme *runtime.Scheme
}

// New returns a Reconciler bound to the given client and scheme.
func New(c client.Client, scheme *runtime.Scheme) *Reconciler {
	return &Reconciler{c: c, scheme: scheme}
}

// EnsureCanaryDeployment creates or updates the canary Deployment (<name>-canary).
// The canary Deployment is a copy of stable with replicas=1 and the label
// kanary.io/revision=canary added to the pod template and selector.
func (r *Reconciler) EnsureCanaryDeployment(
	ctx context.Context,
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
) error {
	want, err := buildCanaryDeployment(canary, stable, r.scheme)
	if err != nil {
		return fmt.Errorf("build canary deployment: %w", err)
	}

	got := &appsv1.Deployment{}
	err = r.c.Get(ctx, client.ObjectKeyFromObject(want), got)
	if apierrors.IsNotFound(err) {
		if err := r.c.Create(ctx, want); err != nil {
			return kerr.Retryable(fmt.Errorf("create canary deployment: %w", err))
		}
		return nil
	}
	if err != nil {
		return kerr.Retryable(fmt.Errorf("get canary deployment: %w", err))
	}

	got.Spec = want.Spec
	got.Labels = want.Labels
	if err := r.c.Update(ctx, got); err != nil {
		return kerr.Retryable(fmt.Errorf("update canary deployment: %w", err))
	}
	return nil
}

// EnsureServices creates or updates the stable Service (<name>) and the canary
// Service (<name>-canary). The stable Service is only created when no Service
// with that name already exists; existing user-managed Services are not taken over.
// Service creation is skipped entirely when canary.Spec.Service.Port == 0.
func (r *Reconciler) EnsureServices(
	ctx context.Context,
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
) error {
	if canary.Spec.Service.Port == 0 {
		return nil
	}

	if err := r.ensureStableService(ctx, canary, stable); err != nil {
		return err
	}
	return r.ensureCanaryService(ctx, canary, stable)
}

// CleanupCanary removes the canary Deployment and canary Service (best-effort).
// The stable Service is left untouched.
func (r *Reconciler) CleanupCanary(ctx context.Context, canary *kanaryv1alpha1.Canary) error {
	name := canary.Spec.TargetRef.Name + "-canary"
	ns := canary.Namespace

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := r.c.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
		return kerr.Retryable(fmt.Errorf("delete canary deployment: %w", err))
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := r.c.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
		return kerr.Retryable(fmt.Errorf("delete canary service: %w", err))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (r *Reconciler) ensureStableService(
	ctx context.Context,
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
) error {
	want, err := buildStableService(canary, stable, r.scheme)
	if err != nil {
		return fmt.Errorf("build stable service: %w", err)
	}

	got := &corev1.Service{}
	err = r.c.Get(ctx, client.ObjectKeyFromObject(want), got)
	if apierrors.IsNotFound(err) {
		if err := r.c.Create(ctx, want); err != nil {
			return kerr.Retryable(fmt.Errorf("create stable service: %w", err))
		}
		return nil
	}
	if err != nil {
		return kerr.Retryable(fmt.Errorf("get stable service: %w", err))
	}
	// Do not overwrite a Service we don't own.
	if got.Labels[kanaryv1alpha1.LabelManaged] != "true" {
		return nil
	}
	got.Spec.Ports = want.Spec.Ports
	if err := r.c.Update(ctx, got); err != nil {
		return kerr.Retryable(fmt.Errorf("update stable service: %w", err))
	}
	return nil
}

func (r *Reconciler) ensureCanaryService(
	ctx context.Context,
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
) error {
	want, err := buildCanaryService(canary, stable, r.scheme)
	if err != nil {
		return fmt.Errorf("build canary service: %w", err)
	}

	got := &corev1.Service{}
	err = r.c.Get(ctx, client.ObjectKeyFromObject(want), got)
	if apierrors.IsNotFound(err) {
		if err := r.c.Create(ctx, want); err != nil {
			return kerr.Retryable(fmt.Errorf("create canary service: %w", err))
		}
		return nil
	}
	if err != nil {
		return kerr.Retryable(fmt.Errorf("get canary service: %w", err))
	}
	got.Spec.Ports = want.Spec.Ports
	got.Spec.Selector = want.Spec.Selector
	if err := r.c.Update(ctx, got); err != nil {
		return kerr.Retryable(fmt.Errorf("update canary service: %w", err))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Object builders
// ---------------------------------------------------------------------------

func buildCanaryDeployment(
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
	scheme *runtime.Scheme,
) (*appsv1.Deployment, error) {
	one := int32(1)

	spec := *stable.Spec.DeepCopy()
	spec.Replicas = &one

	// Add revision label to pod template so the canary Service can select only these pods.
	if spec.Template.Labels == nil {
		spec.Template.Labels = make(map[string]string)
	}
	spec.Template.Labels[kanaryv1alpha1.LabelRevision] = revisionCanary

	// Extend selector to match the new label.
	if spec.Selector == nil {
		spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{}}
	} else {
		sel := maps.Clone(spec.Selector.MatchLabels)
		spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
	}
	spec.Selector.MatchLabels[kanaryv1alpha1.LabelRevision] = revisionCanary

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stable.Name + "-canary",
			Namespace: stable.Namespace,
			Labels: map[string]string{
				kanaryv1alpha1.LabelManaged: "true",
				kanaryv1alpha1.LabelCanary:  canary.Name,
			},
		},
		Spec: spec,
	}
	if err := controllerutil.SetControllerReference(canary, d, scheme); err != nil {
		return nil, err
	}
	return d, nil
}

func buildStableService(
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
	scheme *runtime.Scheme,
) (*corev1.Service, error) {
	sel := map[string]string{}
	if stable.Spec.Selector != nil {
		sel = maps.Clone(stable.Spec.Selector.MatchLabels)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stable.Name,
			Namespace: stable.Namespace,
			Labels: map[string]string{
				kanaryv1alpha1.LabelManaged: "true",
				kanaryv1alpha1.LabelCanary:  canary.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: sel,
			Ports:    servicePorts(canary),
		},
	}
	if err := controllerutil.SetControllerReference(canary, svc, scheme); err != nil {
		return nil, err
	}
	return svc, nil
}

func buildCanaryService(
	canary *kanaryv1alpha1.Canary,
	stable *appsv1.Deployment,
	scheme *runtime.Scheme,
) (*corev1.Service, error) {
	sel := map[string]string{}
	if stable.Spec.Selector != nil {
		sel = maps.Clone(stable.Spec.Selector.MatchLabels)
	}
	sel[kanaryv1alpha1.LabelRevision] = revisionCanary

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stable.Name + "-canary",
			Namespace: stable.Namespace,
			Labels: map[string]string{
				kanaryv1alpha1.LabelManaged: "true",
				kanaryv1alpha1.LabelCanary:  canary.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: sel,
			Ports:    servicePorts(canary),
		},
	}
	if err := controllerutil.SetControllerReference(canary, svc, scheme); err != nil {
		return nil, err
	}
	return svc, nil
}

func servicePorts(canary *kanaryv1alpha1.Canary) []corev1.ServicePort {
	tp := targetPort(canary.Spec.Service.TargetPort, canary.Spec.Service.Port)
	return []corev1.ServicePort{{
		Port:       canary.Spec.Service.Port,
		TargetPort: tp,
	}}
}

func targetPort(s string, fallback int32) intstr.IntOrString {
	if s == "" {
		return intstr.FromInt32(fallback)
	}
	if i, err := strconv.Atoi(s); err == nil {
		return intstr.FromInt32(int32(i)) //#nosec G109 G115 -- port numbers fit in int32
	}
	return intstr.FromString(s)
}
