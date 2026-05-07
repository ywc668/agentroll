/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// optimization_scorecard.go — Sprint 13: Continuous Optimization Loop
//
// Implements:
//   13.1 reconcileOptimizationScorecard (Step 5.12) — recomputes the evolution
//        scorecard and emits a Warning event when a post-promotion regression is
//        detected.
//   13.2 computeOptimizationScorecard — builds OptimizationScorecard from
//        PromptLineage, ToolLineage, and EvalHistory. No API calls needed.
//   13.3 computeQualityTrend — returns "improving", "degrading", or "stable"
//        from the most recent 6 EvalHistory entries (3 older vs 3 recent).
//   13.4 detectRegression — returns true when mean quality dropped >threshold
//        in the post-promotion window vs the pre-promotion baseline.
//   13.5 meanQualityInWindow — mean of EvalHistory scores within a duration window.
//   13.6 mostRecentPromotionTime — finds the most recent promoted entry across
//        PromptLineage and ToolLineage.
//   13.7 incrementOptimizationGeneration — bumps status.evolution.optimizationGeneration;
//        called by promoteVariant and promoteToolExperiment on each successful promotion.

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ─── 13.1 Scorecard reconciler ───────────────────────────────────────────────

// reconcileOptimizationScorecard is Step 5.12 in the reconcile loop.
// Recomputes the evolution scorecard from current lineage and eval history.
// Emits a Warning event when a post-promotion regression is detected.
// Non-fatal — scorecard failures must never block rollouts.
func (r *AgentDeploymentReconciler) reconcileOptimizationScorecard(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	if agentDeploy.Status.Evolution == nil {
		agentDeploy.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{}
	}

	sc := computeOptimizationScorecard(&agentDeploy.Status)
	agentDeploy.Status.Evolution.Scorecard = sc

	if sc.RegressionDetected {
		r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "OptimizationRegression",
			fmt.Sprintf(
				"Quality regression detected after most recent promotion "+
					"(trend: %s, 7d mean: %.3f). Consider reverting the most recent promotion.",
				sc.QualityTrend, sc.MeanQualityLast7Days))
	}

	return nil
}

// ─── 13.2 Scorecard computation ──────────────────────────────────────────────

// computeOptimizationScorecard builds an OptimizationScorecard from the given
// AgentDeploymentStatus. All fields are derived from PromptLineage, ToolLineage,
// and EvalHistory — no API calls needed.
func computeOptimizationScorecard(status *agentrollv1alpha1.AgentDeploymentStatus) *agentrollv1alpha1.OptimizationScorecard {
	sc := &agentrollv1alpha1.OptimizationScorecard{
		UpdatedAt: metav1.Now(),
	}

	for _, e := range status.PromptLineage {
		if e.Outcome == "promoted" {
			sc.PromptPromotions++
		} else {
			sc.PromptRejections++
		}
	}

	for _, e := range status.ToolLineage {
		if e.Outcome == "promoted" {
			sc.ToolPromotions++
		} else {
			sc.ToolRejections++
		}
	}

	sc.QualityTrend = computeQualityTrend(status.EvalHistory)
	sc.MeanQualityLast7Days = meanQualityInWindow(status.EvalHistory, 7*24*time.Hour)

	lastPromotion := mostRecentPromotionTime(status)
	if lastPromotion != nil {
		sc.RegressionDetected = detectRegression(status.EvalHistory, *lastPromotion, 0.10)
	}

	return sc
}

// ─── 13.3 Quality trend ──────────────────────────────────────────────────────

// computeQualityTrend returns "improving", "degrading", or "stable" by comparing
// the mean of the 3 most recent EvalHistory entries against the 3 entries before them.
// Returns "stable" when there are fewer than 6 entries.
//
// Thresholds: delta > +0.02 → improving; delta < -0.02 → degrading; otherwise stable.
func computeQualityTrend(history []agentrollv1alpha1.EvalHistoryEntry) string {
	if len(history) < 6 {
		return "stable"
	}
	n := 3
	older := history[len(history)-2*n : len(history)-n]
	recent := history[len(history)-n:]

	var olderSum, recentSum float64
	for _, e := range older {
		olderSum += e.QualityScore
	}
	for _, e := range recent {
		recentSum += e.QualityScore
	}
	delta := (recentSum - olderSum) / float64(n)
	if delta > 0.02 {
		return "improving"
	}
	if delta < -0.02 {
		return "degrading"
	}
	return "stable"
}

// ─── 13.4 Regression detection ───────────────────────────────────────────────

// detectRegression returns true when the mean quality in the post-promotion
// window has dropped by more than regressionThreshold (e.g. 0.10 = 10%) relative
// to the pre-promotion baseline.
//
// Requires at least 3 EvalHistory entries on each side of lastPromotionAt;
// returns false when there are insufficient entries for comparison.
func detectRegression(
	history []agentrollv1alpha1.EvalHistoryEntry,
	lastPromotionAt metav1.Time,
	regressionThreshold float64,
) bool {
	var pre, post []float64
	for _, e := range history {
		if e.At.Before(&lastPromotionAt) {
			pre = append(pre, e.QualityScore)
		} else {
			post = append(post, e.QualityScore)
		}
	}
	if len(pre) < 3 || len(post) < 3 {
		return false
	}
	var preSum, postSum float64
	for _, v := range pre {
		preSum += v
	}
	for _, v := range post {
		postSum += v
	}
	preMean := preSum / float64(len(pre))
	postMean := postSum / float64(len(post))
	if preMean == 0 {
		return false
	}
	return postMean < preMean*(1-regressionThreshold)
}

// ─── 13.5 Mean quality in window ─────────────────────────────────────────────

// meanQualityInWindow returns the mean QualityScore for EvalHistory entries
// whose At timestamp falls within the last `duration`. Returns 0.0 when no
// entries fall in the window.
func meanQualityInWindow(
	history []agentrollv1alpha1.EvalHistoryEntry,
	duration time.Duration,
) float64 {
	cutoff := time.Now().Add(-duration)
	var sum float64
	var count int
	for _, e := range history {
		if e.At.Time.After(cutoff) {
			sum += e.QualityScore
			count++
		}
	}
	if count == 0 {
		return 0.0
	}
	return sum / float64(count)
}

// ─── 13.6 Most recent promotion time ─────────────────────────────────────────

// mostRecentPromotionTime returns the At timestamp of the most recently promoted
// entry across PromptLineage and ToolLineage. Returns nil when no promotions exist.
func mostRecentPromotionTime(status *agentrollv1alpha1.AgentDeploymentStatus) *metav1.Time {
	var latest *metav1.Time
	for _, e := range status.PromptLineage {
		if e.Outcome == "promoted" {
			t := e.At
			if latest == nil || t.Time.After(latest.Time) {
				latest = &t
			}
		}
	}
	for _, e := range status.ToolLineage {
		if e.Outcome == "promoted" {
			t := e.At
			if latest == nil || t.Time.After(latest.Time) {
				latest = &t
			}
		}
	}
	return latest
}

// ─── 13.7 Generation counter ─────────────────────────────────────────────────

// incrementOptimizationGeneration bumps status.evolution.optimizationGeneration.
// Called by promoteVariant and promoteToolExperiment after each successful promotion.
// Initialises status.Evolution if nil.
func incrementOptimizationGeneration(agentDeploy *agentrollv1alpha1.AgentDeployment) {
	if agentDeploy.Status.Evolution == nil {
		agentDeploy.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{}
	}
	agentDeploy.Status.Evolution.OptimizationGeneration++
}
