/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ── appendEvalHistory ────────────────────────────────────────────────────────

func TestAppendEvalHistory_Basic(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	entry := agentrollv1alpha1.EvalHistoryEntry{
		At:               metav1.Now(),
		CompositeVersion: "v1.gpt4.abc",
		QualityScore:     0.82,
		Verdict:          "pass",
	}
	appendEvalHistory(status, entry)
	if len(status.EvalHistory) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(status.EvalHistory))
	}
	if status.EvalHistory[0].QualityScore != 0.82 {
		t.Errorf("unexpected quality score %f", status.EvalHistory[0].QualityScore)
	}
}

func TestAppendEvalHistory_RingBuffer(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	for i := 0; i < 55; i++ {
		appendEvalHistory(status, agentrollv1alpha1.EvalHistoryEntry{
			At:               metav1.NewTime(time.Now().Add(time.Duration(i) * time.Minute)),
			CompositeVersion: fmt.Sprintf("v%d.gpt4.abc", i),
			QualityScore:     float64(i) / 100.0,
			Verdict:          "pass",
		})
	}
	if len(status.EvalHistory) != 50 {
		t.Errorf("expected history capped at 50, got %d", len(status.EvalHistory))
	}
	// Entries 0–4 should have been evicted; entry 5 is now first.
	if status.EvalHistory[0].CompositeVersion != "v5.gpt4.abc" {
		t.Errorf("expected oldest surviving entry to be v5, got %q",
			status.EvalHistory[0].CompositeVersion)
	}
	if status.EvalHistory[49].CompositeVersion != "v54.gpt4.abc" {
		t.Errorf("expected newest entry to be v54, got %q",
			status.EvalHistory[49].CompositeVersion)
	}
}

func TestAppendEvalHistory_ExactlyAtLimit(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	for i := 0; i < 50; i++ {
		appendEvalHistory(status, agentrollv1alpha1.EvalHistoryEntry{
			CompositeVersion: fmt.Sprintf("v%d", i),
			QualityScore:     0.5,
		})
	}
	appendEvalHistory(status, agentrollv1alpha1.EvalHistoryEntry{
		CompositeVersion: "overflow",
		QualityScore:     0.9,
		Verdict:          "pass",
	})
	if len(status.EvalHistory) != 50 {
		t.Errorf("expected 50 after overflow, got %d", len(status.EvalHistory))
	}
	if status.EvalHistory[49].CompositeVersion != "overflow" {
		t.Errorf("expected newest entry to be overflow, got %q",
			status.EvalHistory[49].CompositeVersion)
	}
}

// ── judgeJobSpec ─────────────────────────────────────────────────────────────

func TestJudgeJobSpec_DefaultModel(t *testing.T) {
	eval := &agentrollv1alpha1.EvaluationSpec{
		SecretRef:     "my-judge-secret",
		JudgeProvider: "anthropic",
		// JudgeModel intentionally empty — should use default
		MinScore: "0.7",
	}
	spec := judgeJobSpec(eval, nil)
	container := spec.Template.Spec.Containers[0]

	envMap := map[string]string{}
	for _, e := range container.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["JUDGE_PROVIDER"] != "anthropic" {
		t.Errorf("expected JUDGE_PROVIDER=anthropic, got %q", envMap["JUDGE_PROVIDER"])
	}
	if envMap["JUDGE_MODEL"] != defaultJudgeModel {
		t.Errorf("expected JUDGE_MODEL=%q (default), got %q", defaultJudgeModel, envMap["JUDGE_MODEL"])
	}
	if envMap["MIN_JUDGE_SCORE"] != "0.7" {
		t.Errorf("expected MIN_JUDGE_SCORE=0.7, got %q", envMap["MIN_JUDGE_SCORE"])
	}
}

func TestJudgeJobSpec_SecretRef(t *testing.T) {
	eval := &agentrollv1alpha1.EvaluationSpec{
		SecretRef: "judge-api-secret",
	}
	spec := judgeJobSpec(eval, nil)
	container := spec.Template.Spec.Containers[0]

	var apiKeyEnv *corev1.EnvVar
	for i := range container.Env {
		if container.Env[i].Name == "JUDGE_API_KEY" {
			apiKeyEnv = &container.Env[i]
			break
		}
	}
	if apiKeyEnv == nil {
		t.Fatal("JUDGE_API_KEY env var not found in judge job spec")
	}
	if apiKeyEnv.ValueFrom == nil || apiKeyEnv.ValueFrom.SecretKeyRef == nil {
		t.Fatal("JUDGE_API_KEY should use SecretKeyRef, got literal value")
	}
	if apiKeyEnv.ValueFrom.SecretKeyRef.Name != "judge-api-secret" {
		t.Errorf("expected secret name judge-api-secret, got %q",
			apiKeyEnv.ValueFrom.SecretKeyRef.Name)
	}
}

func TestJudgeJobSpec_ConfigMapEnvVars(t *testing.T) {
	eval := &agentrollv1alpha1.EvaluationSpec{
		SecretRef: "s",
		ConfigMap: "my-eval-config",
	}
	spec := judgeJobSpec(eval, nil)
	container := spec.Template.Spec.Containers[0]

	var rubricEnv, queriesEnv *corev1.EnvVar
	for i := range container.Env {
		switch container.Env[i].Name {
		case "EVAL_RUBRIC":
			rubricEnv = &container.Env[i]
		case "TEST_QUERIES":
			queriesEnv = &container.Env[i]
		}
	}
	if rubricEnv == nil {
		t.Fatal("EVAL_RUBRIC env var not found when ConfigMap is set")
	}
	if rubricEnv.ValueFrom == nil || rubricEnv.ValueFrom.ConfigMapKeyRef == nil {
		t.Fatal("EVAL_RUBRIC should use ConfigMapKeyRef")
	}
	if rubricEnv.ValueFrom.ConfigMapKeyRef.Name != "my-eval-config" {
		t.Errorf("expected configmap name my-eval-config, got %q",
			rubricEnv.ValueFrom.ConfigMapKeyRef.Name)
	}
	if queriesEnv == nil {
		t.Fatal("TEST_QUERIES env var not found when ConfigMap is set")
	}
}
