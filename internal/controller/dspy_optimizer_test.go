/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

func TestBuildDspyExamples_Empty(t *testing.T) {
	result := buildDspyExamples(nil)
	if result != "" {
		t.Errorf("expected empty string for nil history, got %q", result)
	}
}

func TestBuildDspyExamples_WithHistory(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{CompositeVersion: "p1.m1.v1", QualityScore: 0.85, Verdict: "pass"},
		{CompositeVersion: "p2.m1.v1", QualityScore: 0.43, Verdict: "fail"},
	}
	result := buildDspyExamples(history)
	if result == "" {
		t.Fatal("expected non-empty training examples")
	}
	if !strings.Contains(result, "p1.m1.v1") {
		t.Error("expected p1.m1.v1 in examples")
	}
	if !strings.Contains(result, "p2.m1.v1") {
		t.Error("expected p2.m1.v1 in examples")
	}
	if !strings.Contains(result, "0.85") {
		t.Error("expected score 0.85 in examples")
	}
	if !strings.Contains(result, "0.43") {
		t.Error("expected score 0.43 in examples")
	}
}

func TestDspyVariantName_Format(t *testing.T) {
	name := dspyVariantName("my-agent", 1000)
	want := "my-agent-dspy-1000"
	if name != want {
		t.Errorf("got %q, want %q", name, want)
	}
}

func TestDspyVariantName_Uniqueness(t *testing.T) {
	n1 := dspyVariantName("agent", 100)
	n2 := dspyVariantName("agent", 200)
	if n1 == n2 {
		t.Error("expected different names for different timestamps")
	}
}

func TestBuildDspyOptimizerSystemPrompt_NotEmpty(t *testing.T) {
	prompt := buildDspyOptimizerSystemPrompt()
	if prompt == "" {
		t.Error("expected non-empty system prompt")
	}
	if !strings.Contains(strings.ToLower(prompt), "prompt") {
		t.Error("expected 'prompt' keyword in system prompt")
	}
}

func TestBuildDspyOptimizerUserMessage_IncludesExamples(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{CompositeVersion: "p1.m1.v1", QualityScore: 0.9, Verdict: "pass"},
	}
	msg := buildDspyOptimizerUserMessage("current system prompt text", history)
	if !strings.Contains(msg, "p1.m1.v1") {
		t.Error("expected version in user message")
	}
	if !strings.Contains(msg, "current system prompt text") {
		t.Error("expected current prompt in user message")
	}
}

func TestBuildDspyOptimizerUserMessage_EmptyCurrentPrompt(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{CompositeVersion: "p1.m1.v1", QualityScore: 0.9, Verdict: "pass"},
	}
	msg := buildDspyOptimizerUserMessage("", history)
	if msg == "" {
		t.Error("expected non-empty user message even with empty current prompt")
	}
}

func TestCreateDspyPromptVariant_CreatesVariant(t *testing.T) {
	scheme := makeRolloutsScheme()
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			AgentMeta: agentrollv1alpha1.AgentMetaSpec{PromptVersion: "v3"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentDeploymentReconciler{Client: fakeClient, Scheme: scheme}

	variantName, err := r.createDspyPromptVariant(context.Background(), ad, "new optimized prompt", "old prompt", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if variantName == "" {
		t.Fatal("expected non-empty variant name")
	}

	pv := &agentrollv1alpha1.PromptVariant{}
	if err := fakeClient.Get(context.Background(),
		client.ObjectKey{Name: variantName, Namespace: "default"}, pv); err != nil {
		t.Fatalf("PromptVariant not created: %v", err)
	}
	if pv.Spec.SystemPrompt != "new optimized prompt" {
		t.Errorf("expected new optimized prompt, got %q", pv.Spec.SystemPrompt)
	}
	if pv.Spec.AgentDeploymentRef != "my-agent" {
		t.Errorf("expected AgentDeploymentRef=my-agent, got %q", pv.Spec.AgentDeploymentRef)
	}
	if pv.Spec.ParentVersion != "v3" {
		t.Errorf("expected ParentVersion=v3, got %q", pv.Spec.ParentVersion)
	}
}

func TestReconcileDspyOptimizer_SkipsWhenExperimentActive(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Evolution: &agentrollv1alpha1.EvolutionSpec{
				Enabled:          true,
				PromptExperiment: "existing-variant",
				Optimizer: &agentrollv1alpha1.EvolutionOptimizerSpec{
					Mode:      "dspy",
					Model:     "claude-haiku-4-5-20251001",
					SecretRef: "creds",
				},
			},
		},
	}
	r := &AgentDeploymentReconciler{}

	proposal, err := r.reconcileDspyOptimizer(context.Background(), ad)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposal != "" {
		t.Errorf("expected empty proposal when experiment already active, got %q", proposal)
	}
}

func TestReconcileDspyOptimizer_SkipsWhenInsufficientSamples(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Evolution: &agentrollv1alpha1.EvolutionSpec{
				Enabled: true,
				Optimizer: &agentrollv1alpha1.EvolutionOptimizerSpec{
					Mode:      "dspy",
					Model:     "claude-haiku-4-5-20251001",
					SecretRef: "creds",
				},
			},
		},
		Status: agentrollv1alpha1.AgentDeploymentStatus{
			EvalHistory: []agentrollv1alpha1.EvalHistoryEntry{
				{CompositeVersion: "v1", QualityScore: 0.8},
			},
		},
	}
	r := &AgentDeploymentReconciler{}

	proposal, err := r.reconcileDspyOptimizer(context.Background(), ad)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposal != "" {
		t.Errorf("expected empty proposal with insufficient samples, got %q", proposal)
	}
}
