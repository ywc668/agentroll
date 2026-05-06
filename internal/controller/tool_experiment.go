/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// tool_experiment.go — Sprint 11: Tool Management
//
// Implements:
//   11.2 ToolExperiment CRD lifecycle (Pending → Testing → Promoted/Rejected)
//   11.4 appendToolLineage — ring-buffer helper capped at 20 entries
//
// Reuses welchTTest, collectVariantScores, collectControlScores, meanAndVariance
// from prompt_experiment.go (same package).

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ─── 11.2 Experiment lifecycle ────────────────────────────────────────────────

// reconcileToolExperiment is Step 5.10 in the reconcile loop.
// It advances the tool A/B experiment state machine for the AgentDeployment.
// No-op when spec.evolution.toolExperiment is empty. Non-fatal.
func (r *AgentDeploymentReconciler) reconcileToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	ev := agentDeploy.Spec.Evolution
	if ev == nil || ev.ToolExperiment == "" {
		return nil
	}
	log := logf.FromContext(ctx)

	experiment := &agentrollv1alpha1.ToolExperiment{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      ev.ToolExperiment,
		Namespace: agentDeploy.Namespace,
	}, experiment); err != nil {
		if errors.IsNotFound(err) {
			r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "ToolExperimentError",
				fmt.Sprintf("ToolExperiment %q not found in namespace %q",
					ev.ToolExperiment, agentDeploy.Namespace))
			return nil
		}
		return fmt.Errorf("fetching ToolExperiment %q: %w", ev.ToolExperiment, err)
	}

	switch experiment.Status.Phase {
	case "", agentrollv1alpha1.ToolExperimentPhasePending:
		log.Info("Starting tool experiment", "experiment", experiment.Name)
		return r.startToolExperiment(ctx, agentDeploy, experiment)
	case agentrollv1alpha1.ToolExperimentPhaseTesting:
		return r.evaluateToolExperiment(ctx, agentDeploy, experiment)
	default:
		// Promoted or Rejected — experiment complete; wait for user to clear toolExperiment.
		return nil
	}
}

// startToolExperiment transitions a Pending ToolExperiment to Testing.
// No ConfigMap needed — tool injection is done by reconcileToolDependencies
// reading the active experiment on subsequent reconciles.
func (r *AgentDeploymentReconciler) startToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	experiment *agentrollv1alpha1.ToolExperiment,
) error {
	now := metav1.Now()
	experiment.Status.Phase = agentrollv1alpha1.ToolExperimentPhaseTesting
	experiment.Status.ExperimentStartedAt = &now
	experiment.Status.Message = fmt.Sprintf(
		"Testing started: collecting judge scores (min=%d)",
		toolExperimentMinSamples(agentDeploy))
	if err := r.Status().Update(ctx, experiment); err != nil {
		return fmt.Errorf("setting ToolExperiment %q to Testing: %w", experiment.Name, err)
	}

	r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "ToolExperimentStarted",
		"Tool experiment started: testing %q (min samples: %d)",
		experiment.Name, toolExperimentMinSamples(agentDeploy))
	return nil
}

// evaluateToolExperiment collects scores and makes a promotion/rejection decision
// once enough samples have been gathered.
func (r *AgentDeploymentReconciler) evaluateToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	experiment *agentrollv1alpha1.ToolExperiment,
) error {
	log := logf.FromContext(ctx)

	if experiment.Status.ExperimentStartedAt == nil {
		return nil
	}
	startedAt := experiment.Status.ExperimentStartedAt.Time
	minSamples := toolExperimentMinSamples(agentDeploy)

	variantScores := collectVariantScores(agentDeploy.Status.EvalHistory, startedAt)
	controlScores := collectControlScores(agentDeploy.Status.EvalHistory, startedAt)

	experiment.Status.VariantScores = variantScores
	if int32(len(variantScores)) < minSamples {
		log.Info("Awaiting tool experiment variant scores",
			"experiment", experiment.Name,
			"collected", len(variantScores), "needed", minSamples)
		if err := r.Status().Update(ctx, experiment); err != nil {
			return fmt.Errorf("updating ToolExperiment scores: %w", err)
		}
		return nil
	}
	if len(controlScores) < 2 {
		log.Info("Insufficient control scores for t-test — waiting",
			"experiment", experiment.Name, "control", len(controlScores))
		if err := r.Status().Update(ctx, experiment); err != nil {
			return fmt.Errorf("updating ToolExperiment scores: %w", err)
		}
		return nil
	}

	pValue, variantBetter := welchTTest(variantScores, controlScores)
	variantMean, _ := meanAndVariance(variantScores)
	controlMean, _ := meanAndVariance(controlScores)

	experiment.Status.VariantMeanScore = variantMean
	experiment.Status.ControlMeanScore = controlMean
	experiment.Status.PValue = pValue

	log.Info("Tool t-test result",
		"experiment", experiment.Name,
		"variantMean", fmt.Sprintf("%.3f", variantMean),
		"controlMean", fmt.Sprintf("%.3f", controlMean),
		"p", fmt.Sprintf("%.4f", pValue),
		"variantBetter", variantBetter)

	if variantBetter && pValue < 0.05 {
		return r.promoteToolExperiment(ctx, agentDeploy, experiment)
	}
	return r.rejectToolExperiment(ctx, agentDeploy, experiment)
}

// promoteToolExperiment transitions experiment to Promoted, appends to ToolLineage,
// and emits an event recommending the user update spec.agentMeta.toolDependencies.
func (r *AgentDeploymentReconciler) promoteToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	experiment *agentrollv1alpha1.ToolExperiment,
) error {
	now := metav1.Now()
	experiment.Status.Phase = agentrollv1alpha1.ToolExperimentPhasePromoted
	experiment.Status.PromotedAt = &now
	experiment.Status.Message = fmt.Sprintf(
		"Promoted: variant mean %.3f > control mean %.3f with p=%.4f < 0.05",
		experiment.Status.VariantMeanScore, experiment.Status.ControlMeanScore, experiment.Status.PValue)
	if err := r.Status().Update(ctx, experiment); err != nil {
		return fmt.Errorf("setting ToolExperiment %q to Promoted: %w", experiment.Name, err)
	}

	additionalNames := make([]string, 0, len(experiment.Spec.AdditionalTools))
	for _, t := range experiment.Spec.AdditionalTools {
		additionalNames = append(additionalNames, t.Name)
	}
	appendToolLineage(&agentDeploy.Status, agentrollv1alpha1.ToolLineageEntry{
		ExperimentName:  experiment.Name,
		AdditionalTools: additionalNames,
		RemovedTools:    experiment.Spec.RemovedTools,
		Hypothesis:      experiment.Spec.Hypothesis,
		VariantMean:     experiment.Status.VariantMeanScore,
		ControlMean:     experiment.Status.ControlMeanScore,
		PValue:          experiment.Status.PValue,
		Outcome:         "promoted",
		At:              now,
	})

	r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "ToolExperimentPromoted",
		"Tool experiment %q promoted (p=%.4f). Update spec.agentMeta.toolDependencies to adopt changes permanently.",
		experiment.Name, experiment.Status.PValue)
	return nil
}

// rejectToolExperiment transitions experiment to Rejected and appends to ToolLineage.
func (r *AgentDeploymentReconciler) rejectToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	experiment *agentrollv1alpha1.ToolExperiment,
) error {
	now := metav1.Now()
	experiment.Status.Phase = agentrollv1alpha1.ToolExperimentPhaseRejected
	experiment.Status.RejectedAt = &now
	experiment.Status.Message = fmt.Sprintf(
		"Rejected: variant mean %.3f did not significantly exceed control mean %.3f (p=%.4f)",
		experiment.Status.VariantMeanScore, experiment.Status.ControlMeanScore, experiment.Status.PValue)
	if err := r.Status().Update(ctx, experiment); err != nil {
		return fmt.Errorf("setting ToolExperiment %q to Rejected: %w", experiment.Name, err)
	}

	additionalNames := make([]string, 0, len(experiment.Spec.AdditionalTools))
	for _, t := range experiment.Spec.AdditionalTools {
		additionalNames = append(additionalNames, t.Name)
	}
	appendToolLineage(&agentDeploy.Status, agentrollv1alpha1.ToolLineageEntry{
		ExperimentName:  experiment.Name,
		AdditionalTools: additionalNames,
		RemovedTools:    experiment.Spec.RemovedTools,
		Hypothesis:      experiment.Spec.Hypothesis,
		VariantMean:     experiment.Status.VariantMeanScore,
		ControlMean:     experiment.Status.ControlMeanScore,
		PValue:          experiment.Status.PValue,
		Outcome:         "rejected",
		At:              now,
	})

	r.Recorder.Eventf(agentDeploy, corev1.EventTypeNormal, "ToolExperimentRejected",
		"Tool experiment %q rejected (p=%.4f, variantMean=%.3f, controlMean=%.3f)",
		experiment.Name, experiment.Status.PValue,
		experiment.Status.VariantMeanScore, experiment.Status.ControlMeanScore)
	return nil
}

// toolExperimentMinSamples returns the configured minimum sample count, defaulting to 5.
func toolExperimentMinSamples(agentDeploy *agentrollv1alpha1.AgentDeployment) int32 {
	if agentDeploy.Spec.Evolution != nil && agentDeploy.Spec.Evolution.ToolExperimentMinSamples != nil {
		return *agentDeploy.Spec.Evolution.ToolExperimentMinSamples
	}
	return 5
}

// activeToolExperiment fetches the ToolExperiment named in spec.evolution.toolExperiment
// if it is in Testing phase. Returns nil if not configured, not found, or not Testing.
// Called by reconcileToolDependencies to determine live experiment state.
func (r *AgentDeploymentReconciler) activeToolExperiment(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) *agentrollv1alpha1.ToolExperiment {
	if agentDeploy.Spec.Evolution == nil || agentDeploy.Spec.Evolution.ToolExperiment == "" {
		return nil
	}
	experiment := &agentrollv1alpha1.ToolExperiment{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      agentDeploy.Spec.Evolution.ToolExperiment,
		Namespace: agentDeploy.Namespace,
	}, experiment); err != nil {
		return nil
	}
	if experiment.Status.Phase != agentrollv1alpha1.ToolExperimentPhaseTesting {
		return nil
	}
	return experiment
}

// ─── 11.4 Tool lineage ring buffer ───────────────────────────────────────────

// appendToolLineage appends entry to status.toolLineage, capping at 20.
// Oldest entries are evicted when the buffer is full.
func appendToolLineage(
	status *agentrollv1alpha1.AgentDeploymentStatus,
	entry agentrollv1alpha1.ToolLineageEntry,
) {
	const maxToolLineage = 20
	status.ToolLineage = append(status.ToolLineage, entry)
	if len(status.ToolLineage) > maxToolLineage {
		status.ToolLineage = status.ToolLineage[len(status.ToolLineage)-maxToolLineage:]
	}
}
