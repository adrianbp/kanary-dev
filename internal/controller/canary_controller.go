// Package controller contains the Canary reconciler.
//
// State machine (SPEC.md §4.3):
//
//	Idle → Progressing → AwaitingPromotion → Progressing → … → Succeeded
//	                   ↘ RolledBack
//
// Manual mode is the default. Progressive mode is opt-in and adds analysis
// between steps (handled in a follow-up PR; this file leaves an extension seam).
//
// The reconciler is written to be idempotent and generation-aware:
//
//   - Each Reconcile pass reads the Canary, inspects annotations and status,
//     computes one StepDecision, and writes status + provider state.
//   - Requeues use a fixed poll interval so the controller is observable with
//     simple log scraping; longer idle windows save CPU.
//
// Go in Action, 2nd Ed., Ch. 7 (Errors) and Ch. 9 (Concurrency) informed the
// error-wrapping and context-propagation patterns below.
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kanaryv1alpha1 "github.com/adrianbp/kanary-dev/api/v1alpha1"
	"github.com/adrianbp/kanary-dev/internal/analysis"
	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/traffic"
	"github.com/adrianbp/kanary-dev/internal/workload"
)

// Default poll intervals; tuned to keep CPU low on idle canaries.
const (
	requeueIdle        = 60 * time.Second
	requeueProgressing = 10 * time.Second
	requeueAwaiting    = 20 * time.Second
	annotationTrue     = "true"
)

// Event reasons — surfaced as Kubernetes Events (SPEC.md §9.3).
const (
	ReasonCanaryStarted    = "CanaryStarted"
	ReasonStepAdvanced     = "StepAdvanced"
	ReasonPromotionAwaited = "PromotionAwaited"
	ReasonPromotionAbort   = "PromotionAborted"
	ReasonSucceeded        = "CanarySucceeded"
	ReasonRolledBack       = "RolledBack"
	ReasonReconcileError   = "ReconcileError"
	ReasonAnalysisPassed   = "AnalysisPassed"
	ReasonAnalysisFailed   = "AnalysisFailed"
)

// CanaryReconciler reconciles Canary CRs.
type CanaryReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Recorder           record.EventRecorder
	TrafficFactory     *traffic.Factory
	WorkloadReconciler *workload.Reconciler
	// AnalysisEngine runs metric checks for Progressive mode. Nil disables analysis.
	AnalysisEngine *analysis.Engine
	// ControllerOptions is passed to WithOptions; useful for injecting a slow
	// rate limiter in tests so envtest doesn't saturate the CPU.
	ControllerOptions controller.Options
}

// +kubebuilder:rbac:groups=kanary.io,resources=canaries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kanary.io,resources=canaries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kanary.io,resources=canaries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main loop. Each call executes at most one state transition.
func (r *CanaryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("canary", req.NamespacedName)

	canary := &kanaryv1alpha1.Canary{}
	if err := r.Get(ctx, req.NamespacedName, canary); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get canary: %w", err)
	}

	// --- short-circuit paths ------------------------------------------------

	// Toggle via annotation (requirement #6).
	if canary.Annotations[kanaryv1alpha1.AnnotationCanaryEnabled] == "false" {
		logger.V(1).Info("canary feature disabled via annotation; skipping")
		return ctrl.Result{RequeueAfter: requeueIdle}, nil
	}
	// Paused by operator.
	if canary.Annotations[kanaryv1alpha1.AnnotationPaused] == annotationTrue {
		logger.V(1).Info("canary paused via annotation; skipping")
		return ctrl.Result{RequeueAfter: requeueIdle}, nil
	}

	promoteRequested := canary.Annotations[kanaryv1alpha1.AnnotationPromote] == annotationTrue
	abortRequested := canary.Annotations[kanaryv1alpha1.AnnotationAbort] == annotationTrue

	// Fetch target deployment.
	target := &appsv1.Deployment{}
	targetKey := types.NamespacedName{Name: canary.Spec.TargetRef.Name, Namespace: canary.Namespace}
	if err := r.Get(ctx, targetKey, target); err != nil {
		if apierrors.IsNotFound(err) {
			r.Recorder.Eventf(canary, corev1.EventTypeWarning, ReasonReconcileError,
				"target Deployment %q not found", canary.Spec.TargetRef.Name)
			return ctrl.Result{RequeueAfter: requeueIdle}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get target deployment: %w", err)
	}

	// --- analysis (Progressive mode) ----------------------------------------

	// Run metric analysis before decide() so the result can influence the decision.
	var analysisResult *analysis.Result
	if canary.Spec.Strategy.Mode == kanaryv1alpha1.StrategyProgressive &&
		canary.Spec.Analysis.Enabled &&
		r.AnalysisEngine != nil &&
		canary.Status.Phase == kanaryv1alpha1.PhaseAnalyzing {
		res, err := r.AnalysisEngine.Evaluate(ctx, canary)
		if err != nil {
			return requeueOnRetryable(err, "run analysis")
		}
		analysisResult = &res
	}

	// --- state machine ------------------------------------------------------

	decision, nextPhase, weight, reason := r.decide(canary, target, analysisResult)
	logger.V(1).Info("decided",
		"phase", nextPhase,
		"decision", decision.String(),
		"weight", weight,
		"reason", reason,
	)

	// Apply the decision to traffic provider (idempotent).
	router, err := r.TrafficFactory.Router(canary)
	if err != nil {
		r.Recorder.Eventf(canary, corev1.EventTypeWarning, ReasonReconcileError, "traffic router error: %v", err)
		return ctrl.Result{}, err
	}

	switch decision {
	case domain.DecisionRollback:
		if err := router.Reset(ctx, canary); err != nil {
			return requeueOnRetryable(err, "reset traffic")
		}
		if r.WorkloadReconciler != nil {
			if err := r.WorkloadReconciler.CleanupCanary(ctx, canary); err != nil {
				return requeueOnRetryable(err, "cleanup canary workload")
			}
		}
		r.Recorder.Event(canary, corev1.EventTypeWarning, ReasonRolledBack, reason)

	case domain.DecisionPromote:
		// Canary becomes stable: reset traffic then clean up canary workload.
		if err := router.Reset(ctx, canary); err != nil {
			return requeueOnRetryable(err, "reset after promote")
		}
		if r.WorkloadReconciler != nil {
			if err := r.WorkloadReconciler.CleanupCanary(ctx, canary); err != nil {
				return requeueOnRetryable(err, "cleanup canary workload after promote")
			}
		}
		r.Recorder.Event(canary, corev1.EventTypeNormal, ReasonSucceeded, reason)

	case domain.DecisionAdvance, domain.DecisionHold:
		// Only reconcile traffic when there is an active canary; skip on idle/terminal phases
		// to avoid errors when no canary Ingress exists yet.
		if nextPhase == kanaryv1alpha1.PhaseProgressing || nextPhase == kanaryv1alpha1.PhaseAwaitingPromotion {
			if err := router.Reconcile(ctx, canary, weight); err != nil {
				return requeueOnRetryable(err, "reconcile traffic")
			}
		}
		if r.WorkloadReconciler != nil &&
			(nextPhase == kanaryv1alpha1.PhaseAwaitingPromotion || nextPhase == kanaryv1alpha1.PhaseProgressing) {
			if err := r.WorkloadReconciler.EnsureCanaryDeployment(ctx, canary, target); err != nil {
				return requeueOnRetryable(err, "ensure canary deployment")
			}
			if err := r.WorkloadReconciler.EnsureServices(ctx, canary, target); err != nil {
				return requeueOnRetryable(err, "ensure services")
			}
		}
		if decision == domain.DecisionAdvance {
			r.Recorder.Eventf(canary, corev1.EventTypeNormal, ReasonStepAdvanced,
				"advanced to step %d (%d%%)", canary.Status.CurrentStepIndex+1, weight)
		}
		if analysisResult != nil {
			if analysisResult.Passed {
				r.Recorder.Eventf(canary, corev1.EventTypeNormal, ReasonAnalysisPassed,
					"analysis passed (%d checks)", len(analysisResult.Report.Results))
			} else {
				r.Recorder.Eventf(canary, corev1.EventTypeWarning, ReasonAnalysisFailed,
					"analysis failed: %d/%d checks failed (consecutive=%d)",
					analysisResult.FailedChecks, len(analysisResult.Report.Results),
					analysisResult.FailedChecks)
			}
		}
	}

	if promoteRequested || abortRequested {
		origMeta := canary.DeepCopy()
		clearCommandAnnotations(canary)
		if err := r.Patch(ctx, canary, client.MergeFrom(origMeta)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch command annotations: %w", err)
		}
	}

	// --- persist status -----------------------------------------------------

	orig := canary.DeepCopy()
	r.updateStatus(canary, decision, nextPhase, weight, target, analysisResult)
	if err := r.Status().Patch(ctx, canary, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	return ctrl.Result{RequeueAfter: requeueFor(nextPhase)}, nil
}

// decide turns the current observed state into one of four StepDecisions.
// analysisResult is non-nil only when Progressive mode ran analysis this pass.
// The function itself is free of I/O so it remains easy to unit-test.
func (r *CanaryReconciler) decide(
	canary *kanaryv1alpha1.Canary,
	target *appsv1.Deployment,
	analysisResult *analysis.Result,
) (decision domain.StepDecision, nextPhase kanaryv1alpha1.Phase, weight int32, reason string) {
	// Abort annotation wins over everything.
	if canary.Annotations[kanaryv1alpha1.AnnotationAbort] == annotationTrue {
		return domain.DecisionRollback, kanaryv1alpha1.PhaseRolledBack, 0,
			"abort requested via annotation"
	}

	// No steps configured → nothing to do.
	if len(canary.Spec.Strategy.Steps) == 0 {
		return domain.DecisionHold, kanaryv1alpha1.PhaseIdle, 0,
			"no steps configured"
	}

	// Determine if target has a new revision we haven't started yet.
	stable := canary.Status.StableRevision
	observed := deploymentRevision(target)
	if stable == "" {
		// First observation: seed and stay idle until the next spec change.
		return domain.DecisionHold, kanaryv1alpha1.PhaseIdle, 0,
			"seeding stable revision"
	}
	if stable == observed {
		switch canary.Status.Phase {
		case kanaryv1alpha1.PhaseSucceeded:
			return domain.DecisionHold, kanaryv1alpha1.PhaseSucceeded, 0, "no new revision"
		case kanaryv1alpha1.PhaseRolledBack:
			return domain.DecisionHold, kanaryv1alpha1.PhaseRolledBack, 0, "no new revision after rollback"
		default:
			return domain.DecisionHold, kanaryv1alpha1.PhaseIdle, 0, "no new revision"
		}
	}

	// Determine current step.
	stepIdx := int(canary.Status.CurrentStepIndex)
	steps := canary.Spec.Strategy.Steps
	if stepIdx < 0 || stepIdx >= len(steps) {
		return domain.DecisionRollback, kanaryv1alpha1.PhaseFailed, 0,
			fmt.Sprintf("invalid currentStepIndex=%d for %d steps", stepIdx, len(steps))
	}

	// Manual promote annotation
	promote := canary.Annotations[kanaryv1alpha1.AnnotationPromote] == annotationTrue

	switch canary.Spec.Strategy.Mode {
	case kanaryv1alpha1.StrategyProgressive:
		// Abort via annotation always wins.
		// Progressive flow per step:
		//   1. Enter PhaseAnalyzing (hold weight, wait for analysis interval).
		//   2. On next pass, run analysis and either advance or rollback.
		if promote {
			// Manual override: skip analysis and advance/promote immediately.
			if stepIdx+1 >= len(steps) {
				return domain.DecisionPromote, kanaryv1alpha1.PhaseSucceeded,
					steps[len(steps)-1].Weight, "last step promoted (manual override)"
			}
			return domain.DecisionAdvance, kanaryv1alpha1.PhaseProgressing,
				steps[stepIdx+1].Weight, "promote annotation observed"
		}

		if canary.Status.Phase != kanaryv1alpha1.PhaseAnalyzing {
			// First pass at this step: enter analysis window.
			return domain.DecisionHold, kanaryv1alpha1.PhaseAnalyzing,
				steps[stepIdx].Weight, "entering analysis window"
		}

		// Second pass: analysisResult is populated.
		if analysisResult == nil {
			// Analysis not yet run (e.g. engine not wired); stay in Analyzing.
			return domain.DecisionHold, kanaryv1alpha1.PhaseAnalyzing,
				steps[stepIdx].Weight, "waiting for analysis"
		}
		if analysis.ShouldRollback(canary) {
			return domain.DecisionRollback, kanaryv1alpha1.PhaseRolledBack, 0,
				fmt.Sprintf("analysis failed %d consecutive times", canary.Status.FailedChecks)
		}
		if !analysisResult.Passed {
			// Failure but not yet at the rollback threshold: stay and re-analyze.
			return domain.DecisionHold, kanaryv1alpha1.PhaseAnalyzing,
				steps[stepIdx].Weight, "analysis failed, re-analyzing"
		}
		// Analysis passed: advance to next step or promote.
		if stepIdx+1 >= len(steps) {
			return domain.DecisionPromote, kanaryv1alpha1.PhaseSucceeded,
				steps[len(steps)-1].Weight, "all steps passed analysis"
		}
		return domain.DecisionAdvance, kanaryv1alpha1.PhaseProgressing,
			steps[stepIdx+1].Weight, "analysis passed"

	case kanaryv1alpha1.StrategyManual, "":
		if promote {
			if stepIdx+1 >= len(steps) {
				return domain.DecisionPromote, kanaryv1alpha1.PhaseSucceeded,
					steps[len(steps)-1].Weight, "last step promoted"
			}
			return domain.DecisionAdvance, kanaryv1alpha1.PhaseProgressing,
				steps[stepIdx+1].Weight, "promote annotation observed"
		}
		// Hold at current step waiting for promote.
		return domain.DecisionHold, kanaryv1alpha1.PhaseAwaitingPromotion,
			steps[stepIdx].Weight, "awaiting manual promotion"
	}

	// Fallback: should not happen given the Enum validation on StrategyMode.
	return domain.DecisionHold, kanaryv1alpha1.PhaseIdle, 0, "unknown strategy"
}

// updateStatus is the single place where CanaryStatus mutates. Keeping this
// isolated makes reasoning about conflicts and patches easier.
func (r *CanaryReconciler) updateStatus(
	canary *kanaryv1alpha1.Canary,
	decision domain.StepDecision,
	phase kanaryv1alpha1.Phase,
	weight int32,
	target *appsv1.Deployment,
	analysisResult *analysis.Result,
) {
	canary.Status.ObservedGeneration = canary.Generation
	canary.Status.Phase = phase
	canary.Status.CurrentWeight = weight

	switch decision {
	case domain.DecisionAdvance:
		canary.Status.CurrentStepIndex++
		canary.Status.CanaryRevision = deploymentRevision(target)
	case domain.DecisionHold:
		if phase == kanaryv1alpha1.PhaseAwaitingPromotion || phase == kanaryv1alpha1.PhaseProgressing {
			canary.Status.CanaryRevision = deploymentRevision(target)
		}
	case domain.DecisionRollback:
		// Accept the current revision as the new stable baseline so subsequent
		// reconciles don't re-trigger a canary for the same revision diff.
		canary.Status.StableRevision = deploymentRevision(target)
		canary.Status.CurrentStepIndex = 0
		canary.Status.CanaryRevision = ""
	case domain.DecisionPromote:
		canary.Status.StableRevision = deploymentRevision(target)
		canary.Status.CurrentStepIndex = 0
		canary.Status.CanaryRevision = ""
	}
	if canary.Status.StableRevision == "" {
		canary.Status.StableRevision = deploymentRevision(target)
	}

	if analysisResult != nil {
		canary.Status.FailedChecks = analysisResult.FailedChecks
		report := analysisResult.Report
		canary.Status.LastAnalysis = &report
	}

	readyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             string(phase),
		Message:            fmt.Sprintf("phase=%s weight=%d step=%d", phase, weight, canary.Status.CurrentStepIndex),
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: canary.Generation,
	}
	if phase == kanaryv1alpha1.PhaseFailed || phase == kanaryv1alpha1.PhaseRolledBack {
		readyCond.Status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&canary.Status.Conditions, readyCond)
}

// SetupWithManager registers the controller with the manager and scopes the
// watches tightly so memory usage stays low (SPEC.md §8).
func (r *CanaryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(r.ControllerOptions).
		For(&kanaryv1alpha1.Canary{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		))).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// deploymentRevision returns a stable identifier for the pod template.
// Uses the annotation controller populates (`deployment.kubernetes.io/revision`)
// when present, falling back to generation as a last resort.
func deploymentRevision(d *appsv1.Deployment) string {
	if rev, ok := d.Annotations["deployment.kubernetes.io/revision"]; ok && rev != "" {
		return rev
	}
	return fmt.Sprintf("gen-%d", d.Generation)
}

// requeueOnRetryable maps a Retryable error into a ctrl.Result with a backoff;
// permanent errors bubble up so the controller's workqueue records them.
func requeueOnRetryable(err error, op string) (ctrl.Result, error) {
	if errors.Is(err, kerr.ErrRetryable) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return ctrl.Result{}, fmt.Errorf("%s: %w", op, err)
}

// requeueFor picks a poll interval based on phase.
func requeueFor(p kanaryv1alpha1.Phase) time.Duration {
	switch p {
	case kanaryv1alpha1.PhaseProgressing, kanaryv1alpha1.PhaseAnalyzing, kanaryv1alpha1.PhasePromoting:
		return requeueProgressing
	case kanaryv1alpha1.PhaseAwaitingPromotion:
		return requeueAwaiting
	default:
		return requeueIdle
	}
}

func clearCommandAnnotations(canary *kanaryv1alpha1.Canary) {
	if len(canary.Annotations) == 0 {
		return
	}
	delete(canary.Annotations, kanaryv1alpha1.AnnotationPromote)
	delete(canary.Annotations, kanaryv1alpha1.AnnotationAbort)
	if len(canary.Annotations) == 0 {
		canary.Annotations = nil
	}
}
