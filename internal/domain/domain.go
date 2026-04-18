// Package domain holds the shared domain model of Kanary.
//
// Design intent (Go in Action, 2nd Ed., Chapter 11 — Working with Larger
// Projects): the domain package sits at the center of the module and has no
// external dependencies outside the Go standard library. Both controllers and
// providers depend on this package, never the other way around.
package domain

import (
	"fmt"
	"time"
)

// ReconcileID uniquely identifies one reconcile pass; propagated via logger.
type ReconcileID string

// Revision is a short hash of a Deployment PodTemplate used to tell stable
// and canary revisions apart.
type Revision string

// Short returns the first 7 characters of the revision (git-style) for logs.
func (r Revision) Short() string {
	if len(r) <= 7 {
		return string(r)
	}
	return string(r[:7])
}

// StepDecision is the outcome of evaluating a step on a Canary.
type StepDecision int

const (
	// DecisionHold keeps the current weight (e.g. waiting for manual promote).
	DecisionHold StepDecision = iota
	// DecisionAdvance moves to the next step.
	DecisionAdvance
	// DecisionPromote makes the canary the new stable.
	DecisionPromote
	// DecisionRollback reverts traffic and deletes the canary workload.
	DecisionRollback
)

func (d StepDecision) String() string {
	switch d {
	case DecisionHold:
		return "Hold"
	case DecisionAdvance:
		return "Advance"
	case DecisionPromote:
		return "Promote"
	case DecisionRollback:
		return "Rollback"
	default:
		return fmt.Sprintf("Unknown(%d)", d)
	}
}

// TrafficStatus is a point-in-time snapshot from a traffic provider.
type TrafficStatus struct {
	// StableWeight + CanaryWeight == 100 for providers that report precise weights;
	// for boolean providers, weights are 0 or 100.
	StableWeight int32
	CanaryWeight int32
	ObservedAt   time.Time
}

// MetricQuery is a single request sent to a metric provider.
type MetricQuery struct {
	Name  string
	Query string
	// Window is the duration over which the metric is evaluated.
	Window time.Duration
}

// MetricResult is the scalar outcome of a metric query.
type MetricResult struct {
	Value     float64
	Timestamp time.Time
}
