// Package metrics declares the MetricProvider abstraction.
//
// Concrete implementations live in sub-packages (prometheus, datadog,
// dynatrace). Registration follows the same Factory pattern as
// internal/traffic/router.go. See SPEC.md §4.4.
package metrics

import (
	"context"
	"fmt"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

// Provider is the contract every metric backend must satisfy.
type Provider interface {
	// Query executes q and returns the scalar result.
	Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error)

	// HealthCheck is used by the controller startup to fail-fast when a
	// user-configured provider is unreachable.
	HealthCheck(ctx context.Context) error
}

// Factory is the registry of metric providers, keyed by provider type.
type Factory struct {
	providers map[kanaryv1alpha1.MetricProviderType]Provider
}

// NewFactory returns an empty factory; providers register via Register.
func NewFactory() *Factory {
	return &Factory{providers: map[kanaryv1alpha1.MetricProviderType]Provider{}}
}

// Register adds (or replaces) a Provider for the given type.
func (f *Factory) Register(t kanaryv1alpha1.MetricProviderType, p Provider) {
	f.providers[t] = p
}

// Provider returns the registered Provider for the Canary's metric backend,
// or an error wrapping ErrUnsupportedProvider.
func (f *Factory) Provider(c *kanaryv1alpha1.Canary) (Provider, error) {
	t := c.Spec.Analysis.Provider.Type
	p, ok := f.providers[t]
	if !ok {
		return nil, fmt.Errorf("%w: %q", kerr.ErrUnsupportedProvider, t)
	}
	return p, nil
}
