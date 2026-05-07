/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// memory_check_template.go — Sprint 12b: Agent Memory Lifecycle
//
// Creates/updates the agent-memory-check AnalysisTemplate when the agent has
// both a memory backend (spec.memory.backend) and evaluation (spec.evaluation)
// configured. The template runs memory_checker.py as a Job that tests memory
// recall accuracy and writes the score to Langfuse.

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

// defaultMemoryCheckerImage is the container image for the memory_checker.py Job.
const defaultMemoryCheckerImage = "ghcr.io/agentroll/memory-checker:v1"

// defaultMinMemoryRecallScore is used when no tuned threshold is configured.
const defaultMinMemoryRecallScore = "0.7"

// reconcileMemoryCheckTemplate creates or updates the agent-memory-check
// AnalysisTemplate when the agent has both a memory backend and evaluation configured.
func (r *AgentDeploymentReconciler) reconcileMemoryCheckTemplate(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	hasBackend := agentDeploy.Spec.Memory != nil && agentDeploy.Spec.Memory.Backend != nil
	hasEval := agentDeploy.Spec.Evaluation != nil
	if !hasBackend || !hasEval {
		return nil
	}
	log := logf.FromContext(ctx)

	var lf *agentrollv1alpha1.LangfuseSpec
	if agentDeploy.Spec.Observability != nil {
		lf = agentDeploy.Spec.Observability.Langfuse
	}

	template := &rolloutsv1alpha1.AnalysisTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-memory-check",
			Namespace: agentDeploy.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, template, func() error {
		template.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "agentroll",
			"agentroll.dev/template-type":  "memory-check",
		}
		template.Spec = memoryCheckTemplateSpec(agentDeploy, lf)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile agent-memory-check AnalysisTemplate: %w", err)
	}

	log.Info("agent-memory-check AnalysisTemplate reconciled",
		"name", template.Name, "result", result)
	return nil
}

// memoryCheckTemplateSpec returns the AnalysisTemplateSpec for the agent-memory-check template.
func memoryCheckTemplateSpec(
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
		},
		Metrics: []rolloutsv1alpha1.Metric{
			{
				Name: "memory-recall-accuracy",
				Provider: rolloutsv1alpha1.MetricProvider{
					Job: &rolloutsv1alpha1.JobMetric{
						Spec: memoryCheckJobSpec(agentDeploy, lf),
					},
				},
			},
		},
	}
}

// memoryCheckJobSpec builds the Job spec for the memory_checker.py analysis Job.
// Reads min_memory_recall_score from status.evolution.tunedThresholds if available.
func memoryCheckJobSpec(
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) batchv1.JobSpec {
	minScore := defaultMinMemoryRecallScore
	if agentDeploy.Status.Evolution != nil {
		if v, ok := agentDeploy.Status.Evolution.TunedThresholds["min_memory_recall_score"]; ok {
			minScore = v
		}
	}

	eval := agentDeploy.Spec.Evaluation
	judgeModel := "claude-haiku-4-5-20251001"
	if eval.JudgeModel != "" {
		judgeModel = eval.JudgeModel
	}
	judgeProvider := "anthropic"
	if eval.JudgeProvider != "" {
		judgeProvider = eval.JudgeProvider
	}

	backend := agentDeploy.Spec.Memory.Backend

	envVars := []corev1.EnvVar{
		{
			Name:  "AGENT_SERVICE_URL",
			Value: "http://{{args.service-name}}.{{args.namespace}}.svc:{{args.service-port}}",
		},
		{Name: "CANARY_VERSION", Value: "{{args.canary-version}}"},
		{Name: "MIN_MEMORY_RECALL_SCORE", Value: minScore},
		{Name: "JUDGE_MODEL", Value: judgeModel},
		{Name: "JUDGE_PROVIDER", Value: judgeProvider},
		{Name: "MEM0_API_URL", Value: backend.Endpoint},
		{Name: "AGENT_SESSION_ID", Value: backend.SessionID},
		{
			Name: "API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: eval.SecretRef},
					Key:                  "API_KEY",
				},
			},
		},
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
						Name:  "memory-checker",
						Image: defaultMemoryCheckerImage,
						Env:   envVars,
					},
				},
			},
		},
	}
}
