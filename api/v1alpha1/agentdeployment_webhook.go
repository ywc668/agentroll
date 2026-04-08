/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package v1alpha1

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var webhookLog = logf.Log.WithName("agentdeployment-webhook")

// SetupAgentDeploymentWebhookWithManager registers the validating webhook with the manager.
func SetupAgentDeploymentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &AgentDeployment{}).
		WithValidator(&AgentDeploymentCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-agentroll-dev-v1alpha1-agentdeployment,mutating=false,failurePolicy=fail,sideEffects=None,groups=agentroll.dev,resources=agentdeployments,verbs=create;update,versions=v1alpha1,name=vagentdeployment.kb.io,admissionReviewVersions=v1

// AgentDeploymentCustomValidator validates AgentDeployment resources at admission time,
// catching invalid configurations before they reach the reconciler.
type AgentDeploymentCustomValidator struct{}

func (v *AgentDeploymentCustomValidator) ValidateCreate(_ context.Context, ad *AgentDeployment) (admission.Warnings, error) {
	webhookLog.Info("validate create", "name", ad.Name)
	return nil, validateAgentDeploymentSpec(ad.Name, &ad.Spec)
}

func (v *AgentDeploymentCustomValidator) ValidateUpdate(_ context.Context, _, ad *AgentDeployment) (admission.Warnings, error) {
	webhookLog.Info("validate update", "name", ad.Name)
	return nil, validateAgentDeploymentSpec(ad.Name, &ad.Spec)
}

func (v *AgentDeploymentCustomValidator) ValidateDelete(_ context.Context, _ *AgentDeployment) (admission.Warnings, error) {
	return nil, nil
}

// validateAgentDeploymentSpec consolidates all validation rules and returns a combined error.
func validateAgentDeploymentSpec(name string, spec *AgentDeploymentSpec) error {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateCostSpike(spec)...)
	allErrs = append(allErrs, validateScaling(spec)...)
	allErrs = append(allErrs, validateToolDependencies(spec)...)
	allErrs = append(allErrs, validateDependsOn(name, spec)...)
	if len(allErrs) == 0 {
		return nil
	}
	return allErrs.ToAggregate()
}

// validateCostSpike checks that the cost spike threshold is a percentage > 100%.
func validateCostSpike(spec *AgentDeploymentSpec) field.ErrorList {
	if spec.Rollback == nil || spec.Rollback.OnCostSpike == nil {
		return nil
	}
	threshold := spec.Rollback.OnCostSpike.Threshold
	fld := field.NewPath("spec", "rollback", "onCostSpike", "threshold")
	if !strings.HasSuffix(threshold, "%") {
		return field.ErrorList{field.Invalid(fld, threshold, `must be a percentage string, e.g. "200%"`)}
	}
	val, err := strconv.ParseFloat(strings.TrimSuffix(threshold, "%"), 64)
	if err != nil {
		return field.ErrorList{field.Invalid(fld, threshold, `must be a numeric percentage, e.g. "200%"`)}
	}
	if val <= 100 {
		return field.ErrorList{field.Invalid(fld, threshold, "must be greater than 100% to represent a meaningful cost spike threshold")}
	}
	return nil
}

// validateScaling checks that maxReplicas > minReplicas and queueRef is set for queue-depth.
func validateScaling(spec *AgentDeploymentSpec) field.ErrorList {
	if spec.Scaling == nil {
		return nil
	}
	var errs field.ErrorList
	s := spec.Scaling
	scalingFld := field.NewPath("spec", "scaling")
	if s.MaxReplicas <= s.MinReplicas {
		errs = append(errs, field.Invalid(scalingFld.Child("maxReplicas"), s.MaxReplicas,
			fmt.Sprintf("must be greater than minReplicas (%d)", s.MinReplicas)))
	}
	if s.Metric == "queue-depth" && s.QueueRef == nil {
		errs = append(errs, field.Required(scalingFld.Child("queueRef"),
			`queueRef is required when metric is "queue-depth"`))
	}
	return errs
}

// validateToolDependencies checks that all version constraints are valid semver.
func validateToolDependencies(spec *AgentDeploymentSpec) field.ErrorList {
	var errs field.ErrorList
	fld := field.NewPath("spec", "agentMeta", "toolDependencies")
	for i, td := range spec.AgentMeta.ToolDependencies {
		if td.Version == "" {
			continue
		}
		if _, err := semver.NewConstraint(td.Version); err != nil {
			errs = append(errs, field.Invalid(fld.Index(i).Child("version"), td.Version,
				fmt.Sprintf("invalid semver constraint: %v", err)))
		}
	}
	return errs
}

// validateDependsOn checks that an AgentDeployment does not list itself as a dependency.
func validateDependsOn(name string, spec *AgentDeploymentSpec) field.ErrorList {
	var errs field.ErrorList
	fld := field.NewPath("spec", "dependsOn")
	for i, dep := range spec.DependsOn {
		if dep == name {
			errs = append(errs, field.Invalid(fld.Index(i), dep,
				"an AgentDeployment cannot depend on itself"))
		}
	}
	return errs
}
