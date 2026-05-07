/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// tool_check_template.go — Sprint 11: Tool Management
//
// Creates/updates the agent-tool-check AnalysisTemplate when the agent has
// toolDependencies or an active ToolExperiment. The template runs tool_checker.py
// as a Job that verifies MCP tool call success rates via Langfuse trace analysis.

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// defaultToolCheckerImage is the container image for the tool_checker.py Job.
const defaultToolCheckerImage = "ghcr.io/agentroll/tool-checker:v1"

// defaultMinToolSuccessRate is used when no tuned threshold is configured.
const defaultMinToolSuccessRate = "0.8"

// reconcileToolCheckTemplate creates or updates the agent-tool-check AnalysisTemplate
// when the agent has toolDependencies or a ToolExperiment configured.
func (r *AgentDeploymentReconciler) reconcileToolCheckTemplate(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	hasTools := len(agentDeploy.Spec.AgentMeta.ToolDependencies) > 0
	hasExperiment := agentDeploy.Spec.Evolution != nil && agentDeploy.Spec.Evolution.ToolExperiment != ""
	if !hasTools && !hasExperiment {
		return nil
	}
	log := logf.FromContext(ctx)

	var lf *agentrollv1alpha1.LangfuseSpec
	if agentDeploy.Spec.Observability != nil {
		lf = agentDeploy.Spec.Observability.Langfuse
	}

	template := &rolloutsv1alpha1.AnalysisTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-tool-check",
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, template, func() error {
		template.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "agentroll",
			"agentroll.dev/template-type":  "tool-check",
		}
		template.Spec = toolCheckTemplateSpec(agentDeploy, lf)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile agent-tool-check AnalysisTemplate: %w", err)
	}

	log.Info("agent-tool-check AnalysisTemplate reconciled",
		"name", template.Name, "result", result)
	return nil
}

// toolCheckTemplateSpec returns the AnalysisTemplateSpec for the agent-tool-check template.
func toolCheckTemplateSpec(
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) rolloutsv1alpha1.AnalysisTemplateSpec {
	defaultPort := "8080"
	return rolloutsv1alpha1.AnalysisTemplateSpec{
		Args: []rolloutsv1alpha1.Argument{
			{Name: "service-name"},
			{Name: "service-port", Value: &defaultPort},
			{Name: "namespace"},
			{Name: "canary-version"},
			{Name: "stable-version"},
		},
		Metrics: []rolloutsv1alpha1.Metric{
			{
				Name: "tool-success-rate",
				Provider: rolloutsv1alpha1.MetricProvider{
					Job: &rolloutsv1alpha1.JobMetric{
						Spec: toolCheckJobSpec(agentDeploy, lf),
					},
				},
			},
		},
	}
}

// toolCheckJobSpec builds the Job spec for the tool_checker.py analysis Job.
// Reads min_tool_success_rate from status.evolution.tunedThresholds if available.
func toolCheckJobSpec(
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) batchv1.JobSpec {
	minRate := defaultMinToolSuccessRate
	if agentDeploy.Status.Evolution != nil {
		if v, ok := agentDeploy.Status.Evolution.TunedThresholds["min_tool_success_rate"]; ok {
			minRate = v
		}
	}

	envVars := []corev1.EnvVar{
		{
			Name:  "AGENT_SERVICE_URL",
			Value: "http://{{args.service-name}}.{{args.namespace}}.svc:{{args.service-port}}",
		},
		{Name: "MIN_TOOL_SUCCESS_RATE", Value: minRate},
		{Name: "CANARY_VERSION", Value: "{{args.canary-version}}"},
	}

	if lf != nil && lf.SecretRef != "" {
		lfHost := lf.Endpoint
		if lfHost == "" {
			lfHost = defaultLangfuseHost
		}
		envVars = append(envVars,
			corev1.EnvVar{Name: "LANGFUSE_HOST", Value: lfHost},
			corev1.EnvVar{
				Name: "LANGFUSE_PUBLIC_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: lf.SecretRef},
						Key:                  "LANGFUSE_PUBLIC_KEY",
					},
				},
			},
			corev1.EnvVar{
				Name: "LANGFUSE_SECRET_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: lf.SecretRef},
						Key:                  "LANGFUSE_SECRET_KEY",
					},
				},
			},
		)
	}

	backoffLimit := int32(0)
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:  "tool-checker",
						Image: defaultToolCheckerImage,
						Env:   envVars,
					},
				},
			},
		},
	}
}
