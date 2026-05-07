/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

func makeMemoryCheckAgentDeploy() *agentrollv1alpha1.AgentDeployment {
	endpoint := "mem0.memory:8080"
	return &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Memory: &agentrollv1alpha1.MemorySpec{
				Backend: &agentrollv1alpha1.MemoryBackendSpec{
					Endpoint:  endpoint,
					SecretRef: "mem0-creds",
					SessionID: "agent-sess-1",
				},
			},
			Evaluation: &agentrollv1alpha1.EvaluationSpec{
				JudgeModel:    "claude-haiku-4-5-20251001",
				JudgeProvider: "anthropic",
				SecretRef:     "anthropic-creds",
				MinScore:      "0.7",
			},
		},
	}
}

func TestMemoryCheckTemplateSpec_HasArgs(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	spec := memoryCheckTemplateSpec(ad, nil)

	argNames := map[string]bool{}
	for _, a := range spec.Args {
		argNames[a.Name] = true
	}
	for _, required := range []string{"service-name", "service-port", "namespace", "canary-version"} {
		if !argNames[required] {
			t.Errorf("missing required arg %q", required)
		}
	}
}

func TestMemoryCheckTemplateSpec_HasMemoryRecallMetric(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	spec := memoryCheckTemplateSpec(ad, nil)

	if len(spec.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(spec.Metrics))
	}
	if spec.Metrics[0].Name != "memory-recall-accuracy" {
		t.Errorf("expected metric name memory-recall-accuracy, got %q", spec.Metrics[0].Name)
	}
	if spec.Metrics[0].Provider.Job == nil {
		t.Error("expected Job provider, got nil")
	}
}

func TestMemoryCheckJobSpec_DefaultThreshold(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	spec := memoryCheckJobSpec(ad, nil)

	envMap := map[string]string{}
	for _, e := range spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	if got := envMap["MIN_MEMORY_RECALL_SCORE"]; got != "0.7" {
		t.Errorf("expected MIN_MEMORY_RECALL_SCORE=0.7, got %q", got)
	}
}

func TestMemoryCheckJobSpec_TunedThreshold(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	ad.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{
		TunedThresholds: map[string]string{"min_memory_recall_score": "0.85"},
	}
	spec := memoryCheckJobSpec(ad, nil)

	envMap := map[string]string{}
	for _, e := range spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	if got := envMap["MIN_MEMORY_RECALL_SCORE"]; got != "0.85" {
		t.Errorf("expected tuned threshold 0.85, got %q", got)
	}
}

func TestMemoryCheckJobSpec_JudgeEnvVars(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	spec := memoryCheckJobSpec(ad, nil)

	envMap := map[string]string{}
	envHasSecretRef := map[string]string{}
	for _, e := range spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			envHasSecretRef[e.Name] = e.ValueFrom.SecretKeyRef.Name
		}
	}

	if got := envMap["JUDGE_MODEL"]; got != "claude-haiku-4-5-20251001" {
		t.Errorf("expected JUDGE_MODEL=claude-haiku-4-5-20251001, got %q", got)
	}
	if got := envMap["JUDGE_PROVIDER"]; got != "anthropic" {
		t.Errorf("expected JUDGE_PROVIDER=anthropic, got %q", got)
	}
	if got := envHasSecretRef["API_KEY"]; got != "anthropic-creds" {
		t.Errorf("expected API_KEY secretRef=anthropic-creds, got %q", got)
	}
}

func TestMemoryCheckJobSpec_LangfuseEnv(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	lf := &agentrollv1alpha1.LangfuseSpec{
		Endpoint:  "https://cloud.langfuse.com",
		SecretRef: "langfuse-creds",
	}
	spec := memoryCheckJobSpec(ad, lf)

	envHasSecretRef := map[string]string{}
	envMap := map[string]string{}
	for _, e := range spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			envHasSecretRef[e.Name] = e.ValueFrom.SecretKeyRef.Name
		}
	}

	if got := envMap["LANGFUSE_HOST"]; got != "https://cloud.langfuse.com" {
		t.Errorf("expected LANGFUSE_HOST, got %q", got)
	}
	if got := envHasSecretRef["LANGFUSE_PUBLIC_KEY"]; got != "langfuse-creds" {
		t.Errorf("expected LANGFUSE_PUBLIC_KEY secretRef=langfuse-creds, got %q", got)
	}
}

func TestMemoryCheckJobSpec_Mem0EnvVars(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	spec := memoryCheckJobSpec(ad, nil)

	envMap := map[string]string{}
	for _, e := range spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}

	if got := envMap["MEM0_API_URL"]; got != "mem0.memory:8080" {
		t.Errorf("expected MEM0_API_URL=mem0.memory:8080, got %q", got)
	}
	if got := envMap["AGENT_SESSION_ID"]; got != "agent-sess-1" {
		t.Errorf("expected AGENT_SESSION_ID=agent-sess-1, got %q", got)
	}
}

func TestMemoryCheckJobSpec_NoSessionID(t *testing.T) {
	ad := makeMemoryCheckAgentDeploy()
	ad.Spec.Memory.Backend.SessionID = "" // empty session ID
	spec := memoryCheckJobSpec(ad, nil)

	for _, e := range spec.Template.Spec.Containers[0].Env {
		if e.Name == "AGENT_SESSION_ID" {
			t.Error("expected AGENT_SESSION_ID to be absent when SessionID is empty")
		}
	}
}
