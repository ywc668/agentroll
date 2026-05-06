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

// ── welchTTest ────────────────────────────────────────────────────────────────

func TestWelchTTest_Significant(t *testing.T) {
	// variant scores clearly higher than control
	variant := []float64{0.80, 0.85, 0.82, 0.88, 0.79}
	control := []float64{0.61, 0.63, 0.59, 0.62, 0.64}

	pValue, aIsLarger := welchTTest(variant, control)

	if pValue >= 0.05 {
		t.Errorf("expected p < 0.05 for clearly different distributions, got p=%.4f", pValue)
	}
	if !aIsLarger {
		t.Error("expected variant (a) to have larger mean")
	}
}

func TestWelchTTest_NotSignificant(t *testing.T) {
	// scores nearly identical
	variant := []float64{0.70, 0.71, 0.69, 0.70, 0.72}
	control := []float64{0.69, 0.70, 0.71, 0.70, 0.68}

	pValue, _ := welchTTest(variant, control)

	if pValue < 0.05 {
		t.Errorf("expected p >= 0.05 for nearly identical distributions, got p=%.4f", pValue)
	}
}

func TestWelchTTest_InsufficientData(t *testing.T) {
	// Fewer than 2 samples in either group → no decision possible
	tests := []struct {
		a []float64
		b []float64
	}{
		{[]float64{0.8}, []float64{0.6, 0.7}}, // a too small
		{[]float64{0.8, 0.9}, []float64{0.6}}, // b too small
		{[]float64{}, []float64{0.6, 0.7}},    // a empty
	}
	for _, tc := range tests {
		pValue, _ := welchTTest(tc.a, tc.b)
		if pValue != 1.0 {
			t.Errorf("expected p=1.0 for insufficient data, got p=%.4f (a=%v, b=%v)",
				pValue, tc.a, tc.b)
		}
	}
}

func TestWelchTTest_EqualMeans(t *testing.T) {
	// When means are equal, aIsLarger must be false
	a := []float64{0.7, 0.7, 0.7, 0.7, 0.7}
	b := []float64{0.7, 0.7, 0.7, 0.7, 0.7}

	_, aIsLarger := welchTTest(a, b)
	if aIsLarger {
		t.Error("expected aIsLarger=false when means are equal")
	}
}

// ── meanAndVariance ───────────────────────────────────────────────────────────

func TestMeanAndVariance_Basic(t *testing.T) {
	scores := []float64{2.0, 4.0, 6.0}
	mean, variance := meanAndVariance(scores)

	if math.Abs(mean-4.0) > 1e-9 {
		t.Errorf("expected mean=4.0, got %f", mean)
	}
	// Sample variance: ((2-4)² + (4-4)² + (6-4)²) / 2 = (4+0+4)/2 = 4
	if math.Abs(variance-4.0) > 1e-9 {
		t.Errorf("expected variance=4.0, got %f", variance)
	}
}

func TestMeanAndVariance_SingleValue(t *testing.T) {
	mean, variance := meanAndVariance([]float64{5.0})
	if mean != 5.0 {
		t.Errorf("expected mean=5.0, got %f", mean)
	}
	if variance != 0.0 {
		t.Errorf("expected variance=0.0 for single value, got %f", variance)
	}
}

// ── collectVariantScores / collectControlScores ───────────────────────────────

func TestCollectScores_PartitionByTime(t *testing.T) {
	cutoff := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(cutoff.Add(-2 * time.Hour)), QualityScore: 0.5},
		{At: metav1.NewTime(cutoff.Add(-1 * time.Hour)), QualityScore: 0.6},
		{At: metav1.NewTime(cutoff.Add(1 * time.Hour)), QualityScore: 0.8},
		{At: metav1.NewTime(cutoff.Add(2 * time.Hour)), QualityScore: 0.9},
	}

	variant := collectVariantScores(history, cutoff)
	control := collectControlScores(history, cutoff)

	if len(variant) != 2 {
		t.Errorf("expected 2 variant scores, got %d", len(variant))
	}
	if len(control) != 2 {
		t.Errorf("expected 2 control scores, got %d", len(control))
	}
	if variant[0] != 0.8 || variant[1] != 0.9 {
		t.Errorf("unexpected variant scores: %v", variant)
	}
	if control[0] != 0.5 || control[1] != 0.6 {
		t.Errorf("unexpected control scores: %v", control)
	}
}

func TestCollectVariantScores_Empty(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{At: metav1.NewTime(time.Now().Add(-2 * time.Hour)), QualityScore: 0.5},
	}
	// cutoff is in the future → all entries are "before" → no variant scores
	scores := collectVariantScores(history, time.Now().Add(time.Hour))
	if len(scores) != 0 {
		t.Errorf("expected empty variant scores, got %v", scores)
	}
}

// ── appendPromptLineage ───────────────────────────────────────────────────────

func TestAppendPromptLineage_Basic(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	entry := agentrollv1alpha1.PromptLineageEntry{
		VariantName: "v2",
		VariantMean: 0.82,
		ControlMean: 0.71,
		PValue:      0.023,
		Outcome:     "promoted",
		At:          metav1.Now(),
	}
	appendPromptLineage(status, entry)
	if len(status.PromptLineage) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(status.PromptLineage))
	}
	if status.PromptLineage[0].VariantName != "v2" {
		t.Errorf("unexpected variant name %q", status.PromptLineage[0].VariantName)
	}
}

func TestAppendPromptLineage_Cap(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	for i := 0; i < 22; i++ {
		appendPromptLineage(status, agentrollv1alpha1.PromptLineageEntry{
			VariantName: fmt.Sprintf("v%d", i),
			Outcome:     "rejected",
		})
	}
	if len(status.PromptLineage) != 20 {
		t.Errorf("expected lineage capped at 20, got %d", len(status.PromptLineage))
	}
	// Oldest (v0, v1) evicted; v2 is now first
	if status.PromptLineage[0].VariantName != "v2" {
		t.Errorf("expected oldest surviving entry v2, got %q", status.PromptLineage[0].VariantName)
	}
}
