/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// evaluation.go — Sprint 9: LLM-as-Judge Evaluation Foundation
//
// This file implements:
//
//   9.1 reconcileJudgeTemplate — creates/updates the agent-judge-check
//       AnalysisTemplate when spec.evaluation is configured.
//   9.3 reconcileEvalHistory — after each reconcile, queries Langfuse for
//       new judge_quality_score values and appends them to status.evalHistory.
//   9.4 appendEvalHistory — ring-buffer helper capped at maxEvalHistory entries.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// defaultJudgeModel is used when spec.evaluation.judgeModel is empty.
const defaultJudgeModel = "claude-haiku-4-5-20251001"

// defaultJudgeImage is the container image for the judge_runner.py Job.
const defaultJudgeImage = "ghcr.io/agentroll/judge-runner:v1"

// ─── 9.1 Judge AnalysisTemplate ──────────────────────────────────────────────

// reconcileJudgeTemplate creates or updates the agent-judge-check AnalysisTemplate
// when spec.evaluation is configured.
func (r *AgentDeploymentReconciler) reconcileJudgeTemplate(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	if agentDeploy.Spec.Evaluation == nil {
		return nil
	}
	log := logf.FromContext(ctx)
	eval := agentDeploy.Spec.Evaluation

	template := &rolloutsv1alpha1.AnalysisTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-judge-check",
			Namespace: agentDeploy.Namespace,
		},
	}

	var lf *agentrollv1alpha1.LangfuseSpec
	if agentDeploy.Spec.Observability != nil {
		lf = agentDeploy.Spec.Observability.Langfuse
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, template, func() error {
		template.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "agentroll",
			"agentroll.dev/template-type":  "judge",
		}
		template.Spec = judgeTemplateSpec(eval, lf)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile agent-judge-check AnalysisTemplate: %w", err)
	}

	log.Info("agent-judge-check AnalysisTemplate reconciled",
		"name", template.Name, "result", result)
	return nil
}

// judgeTemplateSpec returns the AnalysisTemplateSpec for the agent-judge-check template.
func judgeTemplateSpec(
	eval *agentrollv1alpha1.EvaluationSpec,
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
				Name: "judge-quality",
				Provider: rolloutsv1alpha1.MetricProvider{
					Job: &rolloutsv1alpha1.JobMetric{
						Spec: judgeJobSpec(eval, lf),
					},
				},
			},
		},
	}
}

// judgeJobSpec builds the Job spec for the judge_runner.py analysis Job.
// eval must not be nil. lf may be nil (Langfuse score writing is then skipped).
func judgeJobSpec(
	eval *agentrollv1alpha1.EvaluationSpec,
	lf *agentrollv1alpha1.LangfuseSpec,
) batchv1.JobSpec {
	judgeModel := eval.JudgeModel
	if judgeModel == "" {
		judgeModel = defaultJudgeModel
	}
	judgeProvider := eval.JudgeProvider
	if judgeProvider == "" {
		judgeProvider = "anthropic"
	}
	minScore := eval.MinScore
	if minScore == "" {
		minScore = "0.7"
	}

	// Base env vars — always present.
	envVars := []corev1.EnvVar{
		{
			Name:  "AGENT_SERVICE_URL",
			Value: "http://{{args.service-name}}.{{args.namespace}}.svc:{{args.service-port}}",
		},
		{Name: "JUDGE_PROVIDER", Value: judgeProvider},
		{Name: "JUDGE_MODEL", Value: judgeModel},
		{
			Name: "JUDGE_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: eval.SecretRef},
					Key:                  "API_KEY",
				},
			},
		},
		{Name: "MIN_JUDGE_SCORE", Value: minScore},
		{Name: "CANARY_VERSION", Value: "{{args.canary-version}}"},
	}

	// Optional: inject rubric and test queries from the eval ConfigMap.
	if eval.ConfigMap != "" {
		optional := true
		envVars = append(envVars,
			corev1.EnvVar{
				Name: "EVAL_RUBRIC",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: eval.ConfigMap},
						Key:                  "rubric",
						Optional:             &optional,
					},
				},
			},
			corev1.EnvVar{
				Name: "TEST_QUERIES",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: eval.ConfigMap},
						Key:                  "test_prompts",
						Optional:             &optional,
					},
				},
			},
		)
	}

	// Optional: inject Langfuse credentials so the runner can write scores.
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
						Name:  "judge",
						Image: defaultJudgeImage,
						Env:   envVars,
					},
				},
			},
		},
	}
}

// ─── 9.3 Eval history sink ────────────────────────────────────────────────────

// reconcileEvalHistory queries Langfuse for recent judge_quality_score values and
// appends new entries (not already in status.evalHistory) to the ring buffer.
// Skipped silently when Langfuse is not configured.
func (r *AgentDeploymentReconciler) reconcileEvalHistory(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	if agentDeploy.Spec.Observability == nil || agentDeploy.Spec.Observability.Langfuse == nil {
		return nil
	}
	log := logf.FromContext(ctx)

	entries, err := r.fetchJudgeScoresFromLangfuse(ctx, agentDeploy,
		agentDeploy.Spec.Observability.Langfuse)
	if err != nil {
		return fmt.Errorf("fetching judge scores from Langfuse: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	// Build a set of already-seen (compositeVersion, qualityScore) pairs to
	// avoid duplicate entries when the controller reconciles multiple times.
	type seen struct {
		version string
		score   float64
	}
	alreadySeen := map[seen]bool{}
	for _, e := range agentDeploy.Status.EvalHistory {
		alreadySeen[seen{e.CompositeVersion, e.QualityScore}] = true
	}

	newCount := 0
	for _, entry := range entries {
		k := seen{entry.CompositeVersion, entry.QualityScore}
		if alreadySeen[k] {
			continue
		}
		appendEvalHistory(&agentDeploy.Status, entry)
		alreadySeen[k] = true
		newCount++
	}

	if newCount > 0 {
		log.Info("Appended new eval history entries",
			"count", newCount, "total", len(agentDeploy.Status.EvalHistory))
	}
	return nil
}

// fetchJudgeScoresFromLangfuse queries GET /api/public/scores?name=judge_quality_score
// and returns EvalHistoryEntry slice.
func (r *AgentDeploymentReconciler) fetchJudgeScoresFromLangfuse(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) ([]agentrollv1alpha1.EvalHistoryEntry, error) {
	if lf.SecretRef == "" {
		return nil, nil
	}

	publicKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_PUBLIC_KEY")
	if err != nil {
		return nil, err
	}
	secretKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_SECRET_KEY")
	if err != nil {
		return nil, err
	}

	host := lf.Endpoint
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	host = strings.TrimRight(host, "/")

	reqURL := fmt.Sprintf("%s/api/public/scores?name=judge_quality_score&limit=20", host)
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(publicKey, secretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Langfuse scores API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Name      string  `json:"name"`
			Value     float64 `json:"value"`
			Comment   string  `json:"comment"`
			CreatedAt string  `json:"createdAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing Langfuse scores response: %w", err)
	}

	entries := make([]agentrollv1alpha1.EvalHistoryEntry, 0, len(result.Data))
	for _, s := range result.Data {
		compositeVersion := parseCompositeVersionFromComment(s.Comment)
		if compositeVersion == "" {
			continue
		}

		var at metav1.Time
		if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			at = metav1.NewTime(t)
		} else {
			at = metav1.Now()
		}

		minScore := 0.7
		if agentDeploy.Spec.Evaluation != nil && agentDeploy.Spec.Evaluation.MinScore != "" {
			if parsed, err := parseFloat64(agentDeploy.Spec.Evaluation.MinScore); err == nil {
				minScore = parsed
			}
		}
		verdict := "fail"
		if s.Value >= minScore {
			verdict = "pass"
		}

		entries = append(entries, agentrollv1alpha1.EvalHistoryEntry{
			At:               at,
			CompositeVersion: compositeVersion,
			QualityScore:     s.Value,
			Verdict:          verdict,
		})
	}
	return entries, nil
}

// parseCompositeVersionFromComment extracts the composite version from a
// judge_quality_score comment formatted as "cv=<version>".
func parseCompositeVersionFromComment(comment string) string {
	for _, part := range strings.Fields(comment) {
		if strings.HasPrefix(part, "cv=") {
			return strings.TrimPrefix(part, "cv=")
		}
	}
	return ""
}

// parseFloat64 is a thin wrapper around fmt.Sscanf for float parsing.
func parseFloat64(s string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	return v, err
}

// ─── 9.4 Eval history ring buffer ────────────────────────────────────────────

// appendEvalHistory appends entry to status.evalHistory, capping at maxEvalHistory.
// Oldest entries are evicted when the buffer is full.
func appendEvalHistory(
	status *agentrollv1alpha1.AgentDeploymentStatus,
	entry agentrollv1alpha1.EvalHistoryEntry,
) {
	const maxEvalHistory = 50
	status.EvalHistory = append(status.EvalHistory, entry)
	if len(status.EvalHistory) > maxEvalHistory {
		status.EvalHistory = status.EvalHistory[len(status.EvalHistory)-maxEvalHistory:]
	}
}
