/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ── computeQualityTrend ───────────────────────────────────────────────────────

func TestComputeQualityTrend_Improving(t *testing.T) {
	// older 3 mean = 0.62, recent 3 mean = 0.72 → delta = +0.10 → improving
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{QualityScore: 0.60}, {QualityScore: 0.62}, {QualityScore: 0.64},
		{QualityScore: 0.70}, {QualityScore: 0.72}, {QualityScore: 0.74},
	}
	if computeQualityTrend(history) != "improving" {
		t.Errorf("expected improving, got %s", computeQualityTrend(history))
	}
}

func TestComputeQualityTrend_Degrading(t *testing.T) {
	// older 3 mean = 0.78, recent 3 mean = 0.58 → delta = -0.20 → degrading
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{QualityScore: 0.80}, {QualityScore: 0.78}, {QualityScore: 0.76},
		{QualityScore: 0.60}, {QualityScore: 0.58}, {QualityScore: 0.56},
	}
	if computeQualityTrend(history) != "degrading" {
		t.Errorf("expected degrading, got %s", computeQualityTrend(history))
	}
}

func TestComputeQualityTrend_Stable(t *testing.T) {
	// All identical scores → delta = 0 → stable
	history := make([]agentrollv1alpha1.EvalHistoryEntry, 6)
	for i := range history {
		history[i].QualityScore = 0.75
	}
	if computeQualityTrend(history) != "stable" {
		t.Errorf("expected stable, got %s", computeQualityTrend(history))
	}
}

func TestComputeQualityTrend_InsufficientData(t *testing.T) {
	// Fewer than 6 entries → stable (not enough to compare windows)
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{QualityScore: 0.8}, {QualityScore: 0.6},
	}
	if computeQualityTrend(history) != "stable" {
		t.Errorf("expected stable for insufficient data, got %s", computeQualityTrend(history))
	}
}

// ── detectRegression ──────────────────────────────────────────────────────────

func TestDetectRegression_Detected(t *testing.T) {
	// pre mean = 0.80, post mean = 0.65 → 18.75% drop → regression at 10% threshold
	pivot := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(pivot.Time.Add(-3 * time.Hour)), QualityScore: 0.80},
		{At: metav1.NewTime(pivot.Time.Add(-2 * time.Hour)), QualityScore: 0.81},
		{At: metav1.NewTime(pivot.Time.Add(-1*time.Hour - time.Minute)), QualityScore: 0.79},
		{At: metav1.NewTime(pivot.Time.Add(time.Minute)), QualityScore: 0.65},
		{At: metav1.NewTime(pivot.Time.Add(2 * time.Hour)), QualityScore: 0.64},
		{At: metav1.NewTime(pivot.Time.Add(3 * time.Hour)), QualityScore: 0.66},
	}
	if !detectRegression(history, pivot, 0.10) {
		t.Error("expected regression detected when post-mean dropped >10% vs pre-mean")
	}
}

func TestDetectRegression_NoRegression(t *testing.T) {
	// post quality better than pre → no regression
	pivot := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(pivot.Time.Add(-3 * time.Hour)), QualityScore: 0.75},
		{At: metav1.NewTime(pivot.Time.Add(-2 * time.Hour)), QualityScore: 0.74},
		{At: metav1.NewTime(pivot.Time.Add(-1*time.Hour - time.Minute)), QualityScore: 0.76},
		{At: metav1.NewTime(pivot.Time.Add(time.Minute)), QualityScore: 0.78},
		{At: metav1.NewTime(pivot.Time.Add(2 * time.Hour)), QualityScore: 0.80},
		{At: metav1.NewTime(pivot.Time.Add(3 * time.Hour)), QualityScore: 0.79},
	}
	if detectRegression(history, pivot, 0.10) {
		t.Error("expected no regression when post-mean is higher than pre-mean")
	}
}

func TestDetectRegression_InsufficientData(t *testing.T) {
	// Only 1 entry before pivot and 1 after → not enough (need ≥3 each side)
	pivot := metav1.Now()
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(pivot.Time.Add(-time.Hour)), QualityScore: 0.80},
		{At: metav1.NewTime(pivot.Time.Add(time.Hour)), QualityScore: 0.50},
	}
	if detectRegression(history, pivot, 0.10) {
		t.Error("expected no regression with insufficient data")
	}
}

// ── meanQualityInWindow ───────────────────────────────────────────────────────

func TestMeanQualityInWindow_Basic(t *testing.T) {
	// 2 entries within 7 days, 1 entry from 10 days ago (outside window)
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour)), QualityScore: 0.5},
		{At: metav1.NewTime(time.Now().Add(-2 * time.Hour)), QualityScore: 0.8},
		{At: metav1.NewTime(time.Now().Add(-30 * time.Minute)), QualityScore: 0.9},
	}
	mean := meanQualityInWindow(history, 7*24*time.Hour)
	expected := (0.8 + 0.9) / 2.0
	if mean < expected-0.001 || mean > expected+0.001 {
		t.Errorf("expected mean %.3f (10-day-old entry excluded), got %.3f", expected, mean)
	}
}

func TestMeanQualityInWindow_Empty(t *testing.T) {
	if meanQualityInWindow(nil, 7*24*time.Hour) != 0.0 {
		t.Error("expected 0.0 for empty history")
	}
}

func TestMeanQualityInWindow_AllOutsideWindow(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour)), QualityScore: 0.9},
	}
	if meanQualityInWindow(history, 7*24*time.Hour) != 0.0 {
		t.Error("expected 0.0 when all entries are outside the window")
	}
}

// ── computeOptimizationScorecard ─────────────────────────────────────────────

func TestComputeOptimizationScorecard_Counts(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{
		PromptLineage: []agentrollv1alpha1.PromptLineageEntry{
			{Outcome: "promoted"},
			{Outcome: "promoted"},
			{Outcome: "rejected"},
		},
		ToolLineage: []agentrollv1alpha1.ToolLineageEntry{
			{Outcome: "promoted"},
			{Outcome: "rejected"},
			{Outcome: "rejected"},
		},
	}
	sc := computeOptimizationScorecard(status)
	if sc.PromptPromotions != 2 {
		t.Errorf("expected 2 prompt promotions, got %d", sc.PromptPromotions)
	}
	if sc.PromptRejections != 1 {
		t.Errorf("expected 1 prompt rejection, got %d", sc.PromptRejections)
	}
	if sc.ToolPromotions != 1 {
		t.Errorf("expected 1 tool promotion, got %d", sc.ToolPromotions)
	}
	if sc.ToolRejections != 2 {
		t.Errorf("expected 2 tool rejections, got %d", sc.ToolRejections)
	}
}

func TestComputeOptimizationScorecard_UpdatedAtSet(t *testing.T) {
	sc := computeOptimizationScorecard(&agentrollv1alpha1.AgentDeploymentStatus{})
	if sc.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be non-zero")
	}
}

// ── incrementOptimizationGeneration ──────────────────────────────────────────

func TestIncrementOptimizationGeneration_InitializesEvolution(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	incrementOptimizationGeneration(ad)
	if ad.Status.Evolution == nil {
		t.Fatal("expected Evolution to be initialized")
	}
	if ad.Status.Evolution.OptimizationGeneration != 1 {
		t.Errorf("expected generation=1, got %d", ad.Status.Evolution.OptimizationGeneration)
	}
}

func TestIncrementOptimizationGeneration_Increments(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{OptimizationGeneration: 5}
	incrementOptimizationGeneration(ad)
	if ad.Status.Evolution.OptimizationGeneration != 6 {
		t.Errorf("expected generation=6, got %d", ad.Status.Evolution.OptimizationGeneration)
	}
}
