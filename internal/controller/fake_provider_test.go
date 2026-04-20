package controller_test

import (
	"context"
	"sync"

	"github.com/adrianbp/kanary-dev/internal/domain"
)

// fakeProvider is a controllable metrics.Provider for envtest scenarios.
// Tests call set() before triggering a reconcile to control what the analysis
// engine will return.
type fakeProvider struct {
	mu    sync.Mutex
	value float64
	err   error
}

func (f *fakeProvider) set(value float64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = value
	f.err = err
}

func (f *fakeProvider) Query(_ context.Context, _ domain.MetricQuery) (domain.MetricResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return domain.MetricResult{Value: f.value}, f.err
}

func (f *fakeProvider) HealthCheck(_ context.Context) error { return nil }
