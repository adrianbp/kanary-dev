package v1alpha1

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-kanary-io-v1alpha1-canary,mutating=true,failurePolicy=fail,sideEffects=None,groups=kanary.io,resources=canaries,verbs=create;update,versions=v1alpha1,name=mcanary.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-kanary-io-v1alpha1-canary,mutating=false,failurePolicy=fail,sideEffects=None,groups=kanary.io,resources=canaries,verbs=create;update,versions=v1alpha1,name=vcanary.kb.io,admissionReviewVersions=v1

// CanaryWebhook implements both the Defaulter and Validator interfaces.
type CanaryWebhook struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &CanaryWebhook{}
var _ webhook.CustomValidator = &CanaryWebhook{}

// SetupWebhookWithManager registers the webhook handlers with the manager.
func (w *CanaryWebhook) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&Canary{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}

// ---------------------------------------------------------------------------
// Defaulting (#012)
// ---------------------------------------------------------------------------

// Default fills in fields that have sensible zero values so the reconciler
// never needs to guard against unset optionals.
func (w *CanaryWebhook) Default(_ context.Context, obj runtime.Object) error {
	c, ok := obj.(*Canary)
	if !ok {
		return fmt.Errorf("expected Canary, got %T", obj)
	}

	if c.Spec.Strategy.Mode == "" {
		c.Spec.Strategy.Mode = StrategyManual
	}

	if c.Spec.Strategy.MaxFailedChecks == 0 {
		c.Spec.Strategy.MaxFailedChecks = 2
	}

	if c.Spec.Strategy.StepInterval.Duration == 0 {
		c.Spec.Strategy.StepInterval.Duration = 2 * time.Minute
	}

	if c.Spec.TargetRef.Kind == "" {
		c.Spec.TargetRef.Kind = "Deployment"
	}

	if c.Spec.TargetRef.APIVersion == "" {
		c.Spec.TargetRef.APIVersion = "apps/v1"
	}

	return nil
}

// ---------------------------------------------------------------------------
// Validation (#011)
// ---------------------------------------------------------------------------

func (w *CanaryWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	c := obj.(*Canary)
	return nil, w.validate(ctx, c)
}

func (w *CanaryWebhook) ValidateUpdate(ctx context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	c := newObj.(*Canary)
	return nil, w.validate(ctx, c)
}

func (w *CanaryWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (w *CanaryWebhook) validate(ctx context.Context, c *Canary) error {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateSteps(c)...)
	allErrs = append(allErrs, validateAnalysis(c)...)

	if err := w.validateTargetRef(ctx, c); err != nil {
		allErrs = append(allErrs, err)
	}

	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(
		GroupVersion.WithKind("Canary").GroupKind(),
		c.Name,
		allErrs,
	)
}

// validateSteps checks that step weights are monotonically increasing and
// within [1, 100], and that the last step reaches 100.
func validateSteps(c *Canary) field.ErrorList {
	var errs field.ErrorList
	steps := c.Spec.Strategy.Steps
	base := field.NewPath("spec", "strategy", "steps")

	for i, s := range steps {
		fp := base.Index(i).Child("weight")
		if s.Weight < 1 || s.Weight > 100 {
			errs = append(errs, field.Invalid(fp, s.Weight,
				"weight must be between 1 and 100"))
		}
		if i > 0 && s.Weight <= steps[i-1].Weight {
			errs = append(errs, field.Invalid(fp, s.Weight,
				fmt.Sprintf("weight must be greater than previous step weight (%d)", steps[i-1].Weight)))
		}
	}

	if len(steps) > 0 && steps[len(steps)-1].Weight != 100 {
		errs = append(errs, field.Invalid(
			base.Index(len(steps)-1).Child("weight"),
			steps[len(steps)-1].Weight,
			"last step weight must be 100",
		))
	}

	return errs
}

// validateAnalysis checks that when analysis is enabled, at least one metric
// is configured and the provider type is set.
func validateAnalysis(c *Canary) field.ErrorList {
	var errs field.ErrorList
	if !c.Spec.Analysis.Enabled {
		return errs
	}
	base := field.NewPath("spec", "analysis")
	if c.Spec.Analysis.Provider.Type == "" {
		errs = append(errs, field.Required(base.Child("provider", "type"),
			"provider type is required when analysis is enabled"))
	}
	if len(c.Spec.Analysis.Metrics) == 0 {
		errs = append(errs, field.Required(base.Child("metrics"),
			"at least one metric is required when analysis is enabled"))
	}
	return errs
}

// validateTargetRef checks that the referenced Deployment exists in the same
// namespace. A not-found error is reported as a field validation failure so
// the user gets a clear message at admission time.
func (w *CanaryWebhook) validateTargetRef(ctx context.Context, c *Canary) *field.Error {
	if w.Client == nil {
		return nil
	}
	fp := field.NewPath("spec", "targetRef", "name")
	deploy := &appsv1.Deployment{}
	key := types.NamespacedName{Name: c.Spec.TargetRef.Name, Namespace: c.Namespace}
	if err := w.Client.Get(ctx, key, deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return field.Invalid(fp, c.Spec.TargetRef.Name,
				fmt.Sprintf("Deployment %q not found in namespace %q", c.Spec.TargetRef.Name, c.Namespace))
		}
		// Transient API errors: allow creation and let the reconciler handle it.
		return nil
	}
	return nil
}
