/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"fmt"
	"math"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ── computeStats ─────────────────────────────────────────────────────────────

func TestComputeStats_BasicMean(t *testing.T) {
	mean, _ := computeStats([]float64{1, 2, 3, 4, 5})
	if math.Abs(mean-3.0) > 1e-9 {
		t.Errorf("expected mean=3.0, got %f", mean)
	}
}

func TestComputeStats_Stddev(t *testing.T) {
	// Population stddev of [2, 4, 4, 4, 5, 5, 7, 9] = 2.0
	_, stddev := computeStats([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if math.Abs(stddev-2.0) > 1e-6 {
		t.Errorf("expected stddev=2.0, got %f", stddev)
	}
}

func TestComputeStats_SingleValue(t *testing.T) {
	mean, stddev := computeStats([]float64{42})
	if mean != 42 {
		t.Errorf("expected mean=42, got %f", mean)
	}
	if stddev != 0 {
		t.Errorf("expected stddev=0, got %f", stddev)
	}
}

func TestComputeStats_AllSame(t *testing.T) {
	mean, stddev := computeStats([]float64{5, 5, 5, 5})
	if mean != 5 {
		t.Errorf("expected mean=5, got %f", mean)
	}
	if stddev != 0 {
		t.Errorf("expected stddev=0, got %f", stddev)
	}
}

// ── isUpperBoundMetric ───────────────────────────────────────────────────────

func TestIsUpperBoundMetric_UpperKeywords(t *testing.T) {
	for _, name := range []string{
		"latency_p99", "token_cost_ratio", "error_rate",
		"LATENCY", "total_tokens", "fail_rate",
	} {
		if !isUpperBoundMetric(name) {
			t.Errorf("expected %q to be an upper-bound metric", name)
		}
	}
}

func TestIsUpperBoundMetric_LowerBound(t *testing.T) {
	for _, name := range []string{
		"quality_score", "tool_success_rate", "response_relevance",
		"content_quality",
	} {
		if isUpperBoundMetric(name) {
			t.Errorf("expected %q to be a lower-bound (quality) metric", name)
		}
	}
}

// ── periodicTriggerDue ───────────────────────────────────────────────────────

func TestPeriodicTriggerDue_NoSchedule(t *testing.T) {
	r := &AgentDeploymentReconciler{}
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Spec.Evolution = &agentrollv1alpha1.EvolutionSpec{
		Enabled:  true,
		Strategy: "all",
		Trigger:  "periodic",
		Schedule: "", // no schedule set
	}
	if r.periodicTriggerDue(ad) {
		t.Error("expected periodicTriggerDue=false when Schedule is empty")
	}
}

func TestPeriodicTriggerDue_NeverRun(t *testing.T) {
	r := &AgentDeploymentReconciler{}
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Spec.Evolution = &agentrollv1alpha1.EvolutionSpec{
		Enabled:  true,
		Strategy: "all",
		Trigger:  "periodic",
		Schedule: "0 2 * * *",
	}
	ad.Status.Evolution = nil // never run
	if !r.periodicTriggerDue(ad) {
		t.Error("expected periodicTriggerDue=true when NextEvalAt is nil (never run)")
	}
}

func TestPeriodicTriggerDue_NotYetDue(t *testing.T) {
	r := &AgentDeploymentReconciler{}
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Spec.Evolution = &agentrollv1alpha1.EvolutionSpec{
		Enabled:  true,
		Strategy: "all",
		Trigger:  "periodic",
		Schedule: "0 2 * * *",
	}
	future := metav1.NewTime(time.Now().Add(1 * time.Hour))
	ad.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{
		NextEvalAt: &future,
	}
	if r.periodicTriggerDue(ad) {
		t.Error("expected periodicTriggerDue=false when NextEvalAt is in the future")
	}
}

func TestPeriodicTriggerDue_Past(t *testing.T) {
	r := &AgentDeploymentReconciler{}
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Spec.Evolution = &agentrollv1alpha1.EvolutionSpec{
		Enabled:  true,
		Strategy: "all",
		Trigger:  "periodic",
		Schedule: "0 2 * * *",
	}
	past := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	ad.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{
		NextEvalAt: &past,
	}
	if !r.periodicTriggerDue(ad) {
		t.Error("expected periodicTriggerDue=true when NextEvalAt is in the past")
	}
}

// ── threshold direction logic (pure math, no cluster needed) ─────────────────

func TestThresholdDirection_UpperBound(t *testing.T) {
	// For upper-bound metrics, new threshold = mean + 1.5*stddev
	vals := []float64{10, 12, 11, 13, 9}
	mean, stddev := computeStats(vals)
	expected := mean + 1.5*stddev
	if expected <= mean {
		t.Errorf("upper bound threshold should be above mean, got expected=%f mean=%f", expected, mean)
	}
}

func TestThresholdDirection_LowerBound(t *testing.T) {
	// For lower-bound metrics, new threshold = mean - 1.5*stddev (clamped at 0)
	vals := []float64{0.80, 0.85, 0.82, 0.88, 0.79}
	mean, stddev := computeStats(vals)
	expected := mean - 1.5*stddev
	if expected >= mean {
		t.Errorf("lower bound threshold should be below mean, got expected=%f mean=%f", expected, mean)
	}
	if expected < 0 {
		t.Errorf("lower bound threshold should be >= 0, got %f", expected)
	}
}

// ── tunedOrDefault ───────────────────────────────────────────────────────────

func TestTunedOrDefault_UsesDefault(t *testing.T) {
	got := tunedOrDefault(nil, "max_latency_ms", "10000")
	if got != "10000" {
		t.Errorf("expected default 10000, got %q", got)
	}
}

func TestTunedOrDefault_UsesTunedValue(t *testing.T) {
	tuned := map[string]string{"max_latency_ms": "7500.0000"}
	got := tunedOrDefault(tuned, "max_latency_ms", "10000")
	if got != "7500.0000" {
		t.Errorf("expected tuned 7500.0000, got %q", got)
	}
}

func TestTunedOrDefault_EmptyValueFallsBack(t *testing.T) {
	tuned := map[string]string{"max_latency_ms": ""}
	got := tunedOrDefault(tuned, "max_latency_ms", "10000")
	if got != "10000" {
		t.Errorf("empty tuned value should fall back to default, got %q", got)
	}
}

// ── appendEvolutionHistory ───────────────────────────────────────────────────

func TestAppendEvolutionHistory_Basic(t *testing.T) {
	st := &agentrollv1alpha1.EvolutionStatus{}
	entry := agentrollv1alpha1.EvolutionHistoryEntry{
		Strategy:    "threshold-tuner",
		Description: "adjusted max_latency_ms→8000",
		Phase:       "Degraded",
	}
	appendEvolutionHistory(st, entry)
	if len(st.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(st.History))
	}
	if st.History[0].Strategy != "threshold-tuner" {
		t.Errorf("unexpected strategy %q", st.History[0].Strategy)
	}
}

func TestAppendEvolutionHistory_RingBuffer(t *testing.T) {
	st := &agentrollv1alpha1.EvolutionStatus{}
	for i := 0; i < 25; i++ {
		appendEvolutionHistory(st, agentrollv1alpha1.EvolutionHistoryEntry{
			Strategy:    "threshold-tuner",
			Description: fmt.Sprintf("run %d", i),
		})
	}
	if len(st.History) != 20 {
		t.Errorf("expected history capped at 20, got %d", len(st.History))
	}
	// The oldest 5 entries (0–4) should have been evicted; entry 5 is now first.
	if st.History[0].Description != "run 5" {
		t.Errorf("expected oldest surviving entry to be 'run 5', got %q", st.History[0].Description)
	}
	if st.History[19].Description != "run 24" {
		t.Errorf("expected newest entry to be 'run 24', got %q", st.History[19].Description)
	}
}

func TestAppendEvolutionHistory_ExactlyAtLimit(t *testing.T) {
	st := &agentrollv1alpha1.EvolutionStatus{}
	for i := 0; i < 20; i++ {
		appendEvolutionHistory(st, agentrollv1alpha1.EvolutionHistoryEntry{
			Strategy: "model-upgrader",
		})
	}
	if len(st.History) != 20 {
		t.Errorf("expected 20 entries, got %d", len(st.History))
	}
	// Adding one more should evict the oldest.
	appendEvolutionHistory(st, agentrollv1alpha1.EvolutionHistoryEntry{
		Strategy:    "prompt-optimizer",
		Description: "last",
	})
	if len(st.History) != 20 {
		t.Errorf("expected still 20 after overflow, got %d", len(st.History))
	}
	if st.History[19].Strategy != "prompt-optimizer" {
		t.Errorf("expected newest to be prompt-optimizer, got %q", st.History[19].Strategy)
	}
}

// ── normalizeLangfuseScoreName ────────────────────────────────────────────────

func TestNormalizeLangfuseScoreName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"avg_latency", "max_latency_ms"},
		{"tool_success_rate", "min_success_rate"},
		{"hallucination_rate", "max_hallucination_rate"},
		{"tool_success_rate_kubectl_get", "min_tool_success_rate_kubectl_get"},
		{"tool_success_rate_kubectl-describe", "min_tool_success_rate_kubectl-describe"},
		{"custom_quality_score", "custom_quality_score"},
	}
	for _, tc := range tests {
		got := normalizeLangfuseScoreName(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeLangfuseScoreName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
