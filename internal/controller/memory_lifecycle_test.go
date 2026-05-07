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

// ── computeDriftScore ─────────────────────────────────────────────────────────

func TestComputeDriftScore_Degrading(t *testing.T) {
	// baseline mean ≈ 0.80, recent mean ≈ 0.64 → drift ≈ -0.20
	snapshots := []agentrollv1alpha1.MemorySnapshotEntry{
		{MeanQualityScore: 0.80},
		{MeanQualityScore: 0.82},
		{MeanQualityScore: 0.78},
		{MeanQualityScore: 0.70},
		{MeanQualityScore: 0.65},
		{MeanQualityScore: 0.62},
	}
	score := computeDriftScore(snapshots)
	if score >= 0 {
		t.Errorf("expected negative drift score for degrading quality, got %.4f", score)
	}
	// Drift should be approximately -0.20 (20% drop)
	if score < -0.30 || score > -0.05 {
		t.Errorf("expected drift score around -0.20, got %.4f", score)
	}
}

func TestComputeDriftScore_Improving(t *testing.T) {
	// baseline mean ≈ 0.60, recent mean ≈ 0.80 → drift > 0
	snapshots := []agentrollv1alpha1.MemorySnapshotEntry{
		{MeanQualityScore: 0.60},
		{MeanQualityScore: 0.62},
		{MeanQualityScore: 0.70},
		{MeanQualityScore: 0.75},
		{MeanQualityScore: 0.78},
		{MeanQualityScore: 0.82},
	}
	score := computeDriftScore(snapshots)
	if score <= 0 {
		t.Errorf("expected positive drift score for improving quality, got %.4f", score)
	}
}

func TestComputeDriftScore_InsufficientSnapshots(t *testing.T) {
	// Fewer than 4 snapshots → cannot split into baseline/recent halves
	snapshots := []agentrollv1alpha1.MemorySnapshotEntry{
		{MeanQualityScore: 0.80},
		{MeanQualityScore: 0.70},
	}
	score := computeDriftScore(snapshots)
	if score != 0.0 {
		t.Errorf("expected 0.0 drift with insufficient snapshots, got %.4f", score)
	}
}

func TestComputeDriftScore_Stable(t *testing.T) {
	// All scores identical → drift = 0
	snapshots := make([]agentrollv1alpha1.MemorySnapshotEntry, 6)
	for i := range snapshots {
		snapshots[i].MeanQualityScore = 0.75
	}
	score := computeDriftScore(snapshots)
	if score != 0.0 {
		t.Errorf("expected 0.0 drift for stable quality, got %.4f", score)
	}
}

// ── appendMemorySnapshot ──────────────────────────────────────────────────────

func TestAppendMemorySnapshot_Basic(t *testing.T) {
	ms := &agentrollv1alpha1.MemoryStatus{}
	entry := agentrollv1alpha1.MemorySnapshotEntry{
		At:               metav1.Now(),
		CompositeVersion: "v1.claude.latest",
		MeanQualityScore: 0.85,
		SampleCount:      5,
	}
	appendMemorySnapshot(ms, entry, 10)
	if len(ms.Snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(ms.Snapshots))
	}
	if ms.Snapshots[0].MeanQualityScore != 0.85 {
		t.Errorf("unexpected quality score %.2f", ms.Snapshots[0].MeanQualityScore)
	}
}

func TestAppendMemorySnapshot_Cap(t *testing.T) {
	ms := &agentrollv1alpha1.MemoryStatus{}
	for i := 0; i < 12; i++ {
		appendMemorySnapshot(ms, agentrollv1alpha1.MemorySnapshotEntry{
			MeanQualityScore: float64(i) * 0.1,
		}, 10)
	}
	if len(ms.Snapshots) != 10 {
		t.Errorf("expected cap at 10 snapshots, got %d", len(ms.Snapshots))
	}
	// First 2 evicted; snapshot[0] should be index 2 → score 0.2
	if ms.Snapshots[0].MeanQualityScore != 0.2 {
		t.Errorf("expected oldest surviving score 0.2, got %.2f", ms.Snapshots[0].MeanQualityScore)
	}
}

// ── shouldTakeSnapshot ────────────────────────────────────────────────────────

func TestShouldTakeSnapshot_FirstSnapshot(t *testing.T) {
	// No last snapshot time → should always take one
	if !shouldTakeSnapshot(nil, 30) {
		t.Error("expected shouldTakeSnapshot=true when no snapshot has been taken")
	}
}

func TestShouldTakeSnapshot_IntervalNotElapsed(t *testing.T) {
	// Last snapshot 5 minutes ago, interval = 30 minutes → not yet
	recent := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	if shouldTakeSnapshot(&recent, 30) {
		t.Error("expected shouldTakeSnapshot=false when interval has not elapsed")
	}
}

func TestShouldTakeSnapshot_IntervalElapsed(t *testing.T) {
	// Last snapshot 35 minutes ago, interval = 30 minutes → yes
	old := metav1.NewTime(time.Now().Add(-35 * time.Minute))
	if !shouldTakeSnapshot(&old, 30) {
		t.Error("expected shouldTakeSnapshot=true when interval has elapsed")
	}
}

// ── recentMeanQuality ─────────────────────────────────────────────────────────

func TestRecentMeanQuality_Basic(t *testing.T) {
	history := []agentrollv1alpha1.EvalHistoryEntry{
		{QualityScore: 0.8},
		{QualityScore: 0.9},
		{QualityScore: 0.7},
	}
	mean := recentMeanQuality(history, 3)
	expected := (0.8 + 0.9 + 0.7) / 3.0
	if mean < expected-0.001 || mean > expected+0.001 {
		t.Errorf("expected mean %.4f, got %.4f", expected, mean)
	}
}

func TestRecentMeanQuality_Empty(t *testing.T) {
	if recentMeanQuality(nil, 5) != 0.0 {
		t.Error("expected 0.0 for empty history")
	}
}
