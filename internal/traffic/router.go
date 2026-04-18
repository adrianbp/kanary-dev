// Package traffic defines the abstraction over traffic-splitting providers.
//
// Providers (Nginx, OpenShift Routes, …) live in sub-packages and are
// registered on the Factory from cmd/manager/main.go. This mirrors the
// "interface at the seam" pattern from Go in Action, 2nd Ed., Chapter 5
// (Working with Types) and keeps the reconciler independent from any
// specific provider implementation.
package traffic

import (
	"context"
	"fmt"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

// Router is implemented by every traffic provider (nginx, openshift-route, …).
//
// Contract:
//
//   - Reconcile(ctx, canary, weight) must be idempotent: calling it twice with
//     the same weight leaves the cluster in the same state.
//   - Reset(ctx, canary) restores the provider to "100% stable" and removes
//     any helper objects.
//   - Status(ctx, canary) reports the observed weight split. The reconciler
//     uses it to detect drift.
type Router interface {
	Reconcile(ctx context.Context, c *kanaryv1alpha1.Canary, weight int32) error
	Reset(ctx context.Context, c *kanaryv1alpha1.Canary) error
	Status(ctx context.Context, c *kanaryv1alpha1.Canary) (domain.TrafficStatus, error)
}

// Factory dispatches to the right Router based on spec.trafficProvider.type.
type Factory struct {
	routers map[kanaryv1alpha1.TrafficProviderType]Router
}

// NewFactory returns an empty factory. Providers are registered by main.
func NewFactory() *Factory {
	return &Factory{routers: map[kanaryv1alpha1.TrafficProviderType]Router{}}
}

// Register adds (or replaces) a Router for the given provider type.
func (f *Factory) Register(t kanaryv1alpha1.TrafficProviderType, r Router) {
	f.routers[t] = r
}

// Router returns the registered Router for the Canary's provider, or an error
// wrapping ErrUnsupportedProvider if none is registered.
func (f *Factory) Router(c *kanaryv1alpha1.Canary) (Router, error) {
	t := c.Spec.TrafficProvider.Type
	r, ok := f.routers[t]
	if !ok {
		return nil, fmt.Errorf("%w: %q", kerr.ErrUnsupportedProvider, t)
	}
	return r, nil
}
