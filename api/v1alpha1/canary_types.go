// Package v1alpha1 — Canary custom resource types.
//
// Design intent (see SPEC.md §4.1): a Canary references an existing Deployment
// and describes how new revisions should be rolled out. We deliberately do NOT
// replace the Deployment with our own workload CRD (requirement #1).
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// Annotations — imperative controls on Canary resources.
// See SPEC.md §4.1.2.
// ---------------------------------------------------------------------------

const (
	// AnnotationPromote advances the canary one step when set to "true".
	AnnotationPromote = "kanary.io/promote"

	// AnnotationAbort triggers an immediate rollback when set to "true".
	AnnotationAbort = "kanary.io/abort"

	// AnnotationPaused pauses reconciliation while set to "true".
	AnnotationPaused = "kanary.io/paused"

	// AnnotationCanaryEnabled toggles the canary logic for a resource.
	// When set to "false", the reconciler treats the target Deployment as
	// if it had no Canary associated and does not manipulate traffic.
	AnnotationCanaryEnabled = "kanary.io/canary-enabled"

	// AnnotationSkipAnalysis skips the metric analysis for the next step.
	AnnotationSkipAnalysis = "kanary.io/skip-analysis"

	// LabelManaged marks objects owned by the Canary reconciler.
	LabelManaged = "kanary.io/managed"

	// LabelCanary identifies which Canary owns a derived object.
	LabelCanary = "kanary.io/canary"

	// LabelRevision distinguishes stable vs canary pods.
	LabelRevision = "kanary.io/revision"
)

// ---------------------------------------------------------------------------
// Enum-like string types.
// ---------------------------------------------------------------------------

// StrategyMode selects between manual and automated rollout.
// +kubebuilder:validation:Enum=Manual;Progressive
type StrategyMode string

const (
	StrategyManual      StrategyMode = "Manual"
	StrategyProgressive StrategyMode = "Progressive"
)

// TrafficProviderType selects the router implementation.
// +kubebuilder:validation:Enum=nginx;openshift-route
type TrafficProviderType string

const (
	TrafficProviderNginx          TrafficProviderType = "nginx"
	TrafficProviderOpenShiftRoute TrafficProviderType = "openshift-route"
)

// MetricProviderType selects the metric backend.
// +kubebuilder:validation:Enum=prometheus;datadog;dynatrace
type MetricProviderType string

const (
	MetricProviderPrometheus MetricProviderType = "prometheus"
	MetricProviderDatadog    MetricProviderType = "datadog"
	MetricProviderDynatrace  MetricProviderType = "dynatrace"
)

// Phase represents the top-level lifecycle state of a Canary.
// +kubebuilder:validation:Enum=Idle;Progressing;AwaitingPromotion;Analyzing;Promoting;Succeeded;Failed;RolledBack
type Phase string

const (
	PhaseIdle              Phase = "Idle"
	PhaseProgressing       Phase = "Progressing"
	PhaseAwaitingPromotion Phase = "AwaitingPromotion"
	PhaseAnalyzing         Phase = "Analyzing"
	PhasePromoting         Phase = "Promoting"
	PhaseSucceeded         Phase = "Succeeded"
	PhaseFailed            Phase = "Failed"
	PhaseRolledBack        Phase = "RolledBack"
)

// ---------------------------------------------------------------------------
// Sub-structs.
// ---------------------------------------------------------------------------

// TargetRef points at the Deployment whose rollouts this Canary manages.
type TargetRef struct {
	// +kubebuilder:validation:Enum=Deployment
	// +kubebuilder:default=Deployment
	Kind string `json:"kind"`

	// +kubebuilder:default=apps/v1
	APIVersion string `json:"apiVersion,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ServiceSpec describes how to expose the target internally.
type ServiceSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// TargetPort can be a port name or number; we accept string for simplicity.
	TargetPort string `json:"targetPort,omitempty"`
}

// TrafficProvider configures how traffic is split between stable and canary.
type TrafficProvider struct {
	Type TrafficProviderType `json:"type"`

	// IngressRef is required when Type=nginx.
	IngressRef *LocalObjectReference `json:"ingressRef,omitempty"`

	// RouteRef is required when Type=openshift-route.
	RouteRef *LocalObjectReference `json:"routeRef,omitempty"`
}

// LocalObjectReference refers to an object in the same namespace as the Canary.
type LocalObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// Step is one point in the weighted rollout schedule.
type Step struct {
	// Weight is the percentage of traffic sent to the canary at this step.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`
}

// Strategy defines how the rollout progresses through its steps.
type Strategy struct {
	// +kubebuilder:default=Manual
	Mode StrategyMode `json:"mode,omitempty"`

	// +kubebuilder:validation:MinItems=1
	Steps []Step `json:"steps"`

	// StepInterval is the minimum time spent on each step (Progressive mode).
	// +kubebuilder:default="2m"
	StepInterval metav1.Duration `json:"stepInterval,omitempty"`

	// MaxFailedChecks is the number of consecutive analysis failures
	// before the reconciler aborts and rolls back (Progressive mode).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	MaxFailedChecks int32 `json:"maxFailedChecks,omitempty"`
}

// MetricCheck describes a single metric the analyzer queries.
type MetricCheck struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`

	ThresholdRange ThresholdRange `json:"thresholdRange"`
}

// ThresholdRange is an acceptance window. At least one of Min/Max must be set.
type ThresholdRange struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// AnalysisConfig is opt-in (requirement #4).
type AnalysisConfig struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	Provider MetricProviderConfig `json:"provider,omitempty"`

	// +kubebuilder:validation:MinItems=1
	Metrics []MetricCheck `json:"metrics,omitempty"`
}

// MetricProviderConfig carries the connection details for a metric backend.
type MetricProviderConfig struct {
	Type MetricProviderType `json:"type"`

	// Address is the HTTP endpoint of the metric provider (ignored for managed SaaS).
	Address string `json:"address,omitempty"`

	// SecretRef points to a Secret holding credentials.
	SecretRef *LocalObjectReference `json:"secretRef,omitempty"`
}

// ---------------------------------------------------------------------------
// Top-level spec/status.
// ---------------------------------------------------------------------------

// CanarySpec is the desired state of a Canary.
type CanarySpec struct {
	TargetRef TargetRef `json:"targetRef"`

	Service ServiceSpec `json:"service"`

	TrafficProvider TrafficProvider `json:"trafficProvider"`

	Strategy Strategy `json:"strategy"`

	// Analysis is opt-in. When disabled the reconciler never queries metrics.
	// +optional
	Analysis AnalysisConfig `json:"analysis,omitempty"`
}

// AnalysisResult captures the outcome of a single metric check.
type AnalysisResult struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Passed bool    `json:"passed"`
}

// AnalysisReport is the last metrics analysis snapshot.
type AnalysisReport struct {
	Timestamp metav1.Time      `json:"timestamp"`
	Results   []AnalysisResult `json:"results,omitempty"`
}

// CanaryStatus is the observed state of a Canary.
type CanaryStatus struct {
	// ObservedGeneration is the .metadata.generation the reconciler has processed.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase Phase `json:"phase,omitempty"`

	CurrentStepIndex int32 `json:"currentStepIndex,omitempty"`

	CurrentWeight int32 `json:"currentWeight,omitempty"`

	StableRevision string `json:"stableRevision,omitempty"`
	CanaryRevision string `json:"canaryRevision,omitempty"`

	FailedChecks int32 `json:"failedChecks,omitempty"`

	LastAnalysis *AnalysisReport `json:"lastAnalysis,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cn,categories={kanary}
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Weight",type=integer,JSONPath=`.status.currentWeight`
// +kubebuilder:printcolumn:name="Step",type=integer,JSONPath=`.status.currentStepIndex`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Canary is a declarative canary rollout for a Deployment target.
type Canary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CanarySpec   `json:"spec,omitempty"`
	Status CanaryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CanaryList is a list of Canary resources.
type CanaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Canary `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Canary{}, &CanaryList{})
}
