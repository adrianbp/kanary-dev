// Package nginx implements traffic.Router for the community ingress-nginx
// controller using its canary annotations.
//
// Reference: https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/annotations/#canary
//
// Design:
//
//   - For each Canary we keep the "stable" Ingress untouched (that is the one
//     the user created) and manage a sibling "canary" Ingress with the same
//     host/path but pointing at the canary Service and the canary annotations.
//   - The weight is updated with an UPDATE, not delete+create, so ingress-nginx
//     never sees a window of zero backends.
//   - Reset deletes only the sibling Ingress and removes the canary annotation
//     set, leaving the user-owned Ingress intact.
package nginx

import (
	"context"
	"fmt"
	"strconv"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

// Annotations understood by ingress-nginx (public constants so tests don't
// hard-code strings).
const (
	AnnotationCanary       = "nginx.ingress.kubernetes.io/canary"
	AnnotationCanaryWeight = "nginx.ingress.kubernetes.io/canary-weight"

	// siblingSuffix is appended to the user-facing Ingress name to produce
	// the sibling canary Ingress name.
	siblingSuffix = "-kanary"
)

// Router is the ingress-nginx implementation of traffic.Router.
type Router struct {
	c client.Client
}

// New returns a Router bound to the given controller-runtime client.
func New(c client.Client) *Router { return &Router{c: c} }

// Reconcile ensures the sibling canary Ingress exists with the requested weight.
func (r *Router) Reconcile(ctx context.Context, canary *kanaryv1alpha1.Canary, weight int32) error {
	if canary.Spec.TrafficProvider.IngressRef == nil {
		return fmt.Errorf("%w: ingressRef is required for nginx provider", kerr.ErrInvalidSpec)
	}
	if weight < 0 || weight > 100 {
		return fmt.Errorf("%w: weight out of range: %d", kerr.ErrInvalidSpec, weight)
	}

	stable := &networkingv1.Ingress{}
	stableKey := types.NamespacedName{
		Name:      canary.Spec.TrafficProvider.IngressRef.Name,
		Namespace: canary.Namespace,
	}
	if err := r.c.Get(ctx, stableKey, stable); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("stable ingress %s: %w", stableKey, kerr.ErrNotFound)
		}
		return kerr.Retryable(fmt.Errorf("get stable ingress: %w", err))
	}

	sibling := SiblingIngress(canary, stable, weight)
	if err := controllerutil.SetControllerReference(canary, sibling, r.c.Scheme()); err != nil {
		return fmt.Errorf("set owner ref: %w", err)
	}

	// CreateOrUpdate: the mutation func keeps the sibling's spec and annotations
	// in sync with the desired weight.
	_, err := controllerutil.CreateOrUpdate(ctx, r.c, sibling, func() error {
		desired := SiblingIngress(canary, stable, weight)
		sibling.Spec = desired.Spec
		if sibling.Annotations == nil {
			sibling.Annotations = map[string]string{}
		}
		for k, v := range desired.Annotations {
			sibling.Annotations[k] = v
		}
		if sibling.Labels == nil {
			sibling.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			sibling.Labels[k] = v
		}
		return nil
	})
	if err != nil {
		return kerr.Retryable(fmt.Errorf("upsert sibling ingress: %w", err))
	}
	return nil
}

// Reset deletes the sibling canary Ingress; the stable Ingress is left alone.
func (r *Router) Reset(ctx context.Context, canary *kanaryv1alpha1.Canary) error {
	if canary.Spec.TrafficProvider.IngressRef == nil {
		return nil
	}
	sibling := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      siblingName(canary.Spec.TrafficProvider.IngressRef.Name),
			Namespace: canary.Namespace,
		},
	}
	if err := r.c.Delete(ctx, sibling); err != nil && !apierrors.IsNotFound(err) {
		return kerr.Retryable(fmt.Errorf("delete sibling ingress: %w", err))
	}
	return nil
}

// Status reads the sibling's canary-weight annotation and reports the observed weight.
func (r *Router) Status(ctx context.Context, canary *kanaryv1alpha1.Canary) (domain.TrafficStatus, error) {
	if canary.Spec.TrafficProvider.IngressRef == nil {
		return domain.TrafficStatus{}, fmt.Errorf("%w: ingressRef required", kerr.ErrInvalidSpec)
	}
	sibling := &networkingv1.Ingress{}
	key := types.NamespacedName{
		Name:      siblingName(canary.Spec.TrafficProvider.IngressRef.Name),
		Namespace: canary.Namespace,
	}
	if err := r.c.Get(ctx, key, sibling); err != nil {
		if apierrors.IsNotFound(err) {
			return domain.TrafficStatus{
				StableWeight: 100,
				CanaryWeight: 0,
				ObservedAt:   time.Now(),
			}, nil
		}
		return domain.TrafficStatus{}, kerr.Retryable(fmt.Errorf("get sibling ingress: %w", err))
	}

	w, _ := strconv.Atoi(sibling.Annotations[AnnotationCanaryWeight])
	return domain.TrafficStatus{
		StableWeight: int32(100 - w),
		CanaryWeight: int32(w),
		ObservedAt:   time.Now(),
	}, nil
}

// SiblingIngress builds the desired state of the canary Ingress (exported
// so tests can assert on the structure).
func SiblingIngress(canary *kanaryv1alpha1.Canary, stable *networkingv1.Ingress, weight int32) *networkingv1.Ingress {
	sib := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      siblingName(stable.Name),
			Namespace: stable.Namespace,
			Labels: map[string]string{
				kanaryv1alpha1.LabelManaged: "true",
				kanaryv1alpha1.LabelCanary:  canary.Name,
			},
			Annotations: map[string]string{
				AnnotationCanary:       "true",
				AnnotationCanaryWeight: strconv.Itoa(int(weight)),
			},
		},
		Spec: *stable.Spec.DeepCopy(),
	}

	// Rewrite every backend service reference in the sibling so that traffic
	// goes to the canary Service instead of the stable one. The canary service
	// name is derived as "<target>-canary" by convention.
	canarySvc := canary.Spec.TargetRef.Name + "-canary"
	for i := range sib.Spec.Rules {
		rule := &sib.Spec.Rules[i]
		if rule.HTTP == nil {
			continue
		}
		for j := range rule.HTTP.Paths {
			p := &rule.HTTP.Paths[j]
			if p.Backend.Service != nil {
				p.Backend.Service.Name = canarySvc
			}
		}
	}
	return sib
}

func siblingName(stableName string) string { return stableName + siblingSuffix }
