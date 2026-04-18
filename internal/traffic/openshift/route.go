// Package openshift implements traffic.Router for OpenShift Routes.
//
// Design:
//
//   - The user owns the stable Route (we never mutate it).
//   - We control the weight split via the Route's `spec.to.weight` (stable)
//     and `spec.alternateBackends[0].weight` (canary).
//   - Weights are integers in [0, 256] per the Route API; we normalise the
//     caller's [0, 100] percentage into that range using integer arithmetic
//     (canary = weight*256/100, stable = 256 - canary).
//   - Reset restores to 100% stable (to.weight=256, alternateBackends=[]).
//   - We use unstructured.Unstructured to avoid importing the full
//     openshift/api module; only the GVR/GVK we need is referenced here.
//
// Reference: https://docs.openshift.com/container-platform/4.14/networking/routes/route-configuration.html
package openshift

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

// RouteGVK is the GroupVersionKind for OpenShift Route objects.
var RouteGVK = schema.GroupVersionKind{
	Group:   "route.openshift.io",
	Version: "v1",
	Kind:    "Route",
}

const (
	// maxRouteWeight is the maximum weight value accepted by the Route API.
	maxRouteWeight = int64(256)
)

// Router is the OpenShift Route implementation of traffic.Router.
type Router struct {
	c client.Client
}

// New returns a Router bound to the given controller-runtime client.
func New(c client.Client) *Router { return &Router{c: c} }

// Reconcile sets the canary weight on the user's Route by writing
// spec.to.weight (stable) and spec.alternateBackends[0].weight (canary).
func (r *Router) Reconcile(ctx context.Context, canary *kanaryv1alpha1.Canary, weight int32) error {
	if canary.Spec.TrafficProvider.RouteRef == nil {
		return fmt.Errorf("%w: routeRef is required for openshift-route provider", kerr.ErrInvalidSpec)
	}
	if weight < 0 || weight > 100 {
		return fmt.Errorf("%w: weight out of range: %d", kerr.ErrInvalidSpec, weight)
	}

	route, err := r.getRoute(ctx, canary)
	if err != nil {
		return err
	}

	canarySvc := canaryServiceName(canary)
	canaryW, stableW := splitWeights(weight)

	if err := setWeights(route, canarySvc, canaryW, stableW); err != nil {
		return fmt.Errorf("%w: set weights: %w", kerr.ErrInvalidSpec, err)
	}

	if err := r.c.Update(ctx, route); err != nil {
		return kerr.Retryable(fmt.Errorf("update route: %w", err))
	}
	return nil
}

// Reset restores the Route to 100% stable by removing alternateBackends and
// setting spec.to.weight back to maxRouteWeight.
func (r *Router) Reset(ctx context.Context, canary *kanaryv1alpha1.Canary) error {
	if canary.Spec.TrafficProvider.RouteRef == nil {
		return nil
	}

	route, err := r.getRoute(ctx, canary)
	if err != nil {
		if errors.Is(err, kerr.ErrNotFound) {
			return nil
		}
		return err
	}

	if err := unstructured.SetNestedField(route.Object,
		maxRouteWeight, "spec", "to", "weight"); err != nil {
		return fmt.Errorf("reset stable weight: %w", err)
	}
	// Remove alternateBackends entirely.
	unstructured.RemoveNestedField(route.Object, "spec", "alternateBackends")

	if err := r.c.Update(ctx, route); err != nil {
		return kerr.Retryable(fmt.Errorf("reset route: %w", err))
	}
	return nil
}

// Status reads the observed canary weight from alternateBackends.
func (r *Router) Status(ctx context.Context, canary *kanaryv1alpha1.Canary) (domain.TrafficStatus, error) {
	if canary.Spec.TrafficProvider.RouteRef == nil {
		return domain.TrafficStatus{}, fmt.Errorf("%w: routeRef required", kerr.ErrInvalidSpec)
	}

	route, err := r.getRoute(ctx, canary)
	if err != nil {
		if errors.Is(err, kerr.ErrNotFound) {
			return domain.TrafficStatus{StableWeight: 100, ObservedAt: time.Now()}, nil
		}
		return domain.TrafficStatus{}, kerr.Retryable(fmt.Errorf("get route for status: %w", err))
	}

	canaryW, stableW := observedWeights(route)
	return domain.TrafficStatus{
		CanaryWeight: canaryW,
		StableWeight: stableW,
		ObservedAt:   time.Now(),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *Router) getRoute(ctx context.Context, canary *kanaryv1alpha1.Canary) (*unstructured.Unstructured, error) {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(RouteGVK)

	key := types.NamespacedName{
		Name:      canary.Spec.TrafficProvider.RouteRef.Name,
		Namespace: canary.Namespace,
	}
	if err := r.c.Get(ctx, key, route); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("route %s: %w", key, kerr.ErrNotFound)
		}
		return nil, kerr.Retryable(fmt.Errorf("get route: %w", err))
	}
	return route, nil
}

// splitWeights converts a [0,100] canary percentage into Route API weights.
// canaryW + stableW == maxRouteWeight (256).
func splitWeights(canaryPct int32) (canaryW, stableW int64) {
	canaryW = int64(canaryPct) * maxRouteWeight / 100
	stableW = maxRouteWeight - canaryW
	return
}

// setWeights writes spec.to.weight and spec.alternateBackends into the Route.
func setWeights(route *unstructured.Unstructured, canarySvc string, canaryW, stableW int64) error {
	if err := unstructured.SetNestedField(route.Object, stableW, "spec", "to", "weight"); err != nil {
		return err
	}
	alt := map[string]interface{}{
		"kind":   "Service",
		"name":   canarySvc,
		"weight": canaryW,
	}
	return unstructured.SetNestedSlice(route.Object, []interface{}{alt}, "spec", "alternateBackends")
}

// observedWeights extracts [0,100] weights from the current Route object.
func observedWeights(route *unstructured.Unstructured) (canaryW, stableW int32) {
	alts, _, _ := unstructured.NestedSlice(route.Object, "spec", "alternateBackends")
	if len(alts) == 0 {
		return 0, 100
	}
	alt, ok := alts[0].(map[string]interface{})
	if !ok {
		return 0, 100
	}
	rawW, _, _ := unstructured.NestedInt64(alt, "weight")
	if maxRouteWeight == 0 {
		return 0, 100
	}
	pct64 := rawW * 100 / maxRouteWeight
	if pct64 < 0 {
		pct64 = 0
	} else if pct64 > 100 {
		pct64 = 100
	}
	pct := int32(pct64) //#nosec G115
	return pct, 100 - pct
}

// canaryServiceName derives the canary Service name from the Canary spec.
func canaryServiceName(c *kanaryv1alpha1.Canary) string {
	return c.Spec.TargetRef.Name + "-canary"
}
