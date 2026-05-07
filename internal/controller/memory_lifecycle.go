/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// memory_lifecycle.go — Sprint 12: Agent Memory Lifecycle
//
// Implements:
//   12.1 reconcileMemoryLifecycle (Step 5.11) — periodic quality snapshots +
//        drift detection from EvalHistory trends.
//   12.2 computeDriftScore — compares recent snapshot mean to baseline mean.
//   12.3 appendMemorySnapshot — ring-buffer helper.
//   12.4 shouldTakeSnapshot — interval gate.
//   12.5 recentMeanQuality — computes mean from the latest N EvalHistory entries.

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ─── 12.1 Memory lifecycle reconciler ────────────────────────────────────────

// reconcileMemoryLifecycle is Step 5.11 in the reconcile loop.
// Takes a periodic quality snapshot and checks for memory drift.
// No-op when spec.memory is nil or snapshotEnabled is false. Non-fatal.
func (r *AgentDeploymentReconciler) reconcileMemoryLifecycle(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	if agentDeploy.Spec.Memory == nil || !agentDeploy.Spec.Memory.SnapshotEnabled {
		return nil
	}
	log := logf.FromContext(ctx)
	mem := agentDeploy.Spec.Memory

	// Determine interval and max snapshots (use spec or kubebuilder defaults).
	intervalMinutes := int32(30)
	if mem.SnapshotIntervalMinutes != nil {
		intervalMinutes = *mem.SnapshotIntervalMinutes
	}
	maxSnapshots := int32(10)
	if mem.MaxSnapshots != nil {
		maxSnapshots = *mem.MaxSnapshots
	}

	// Initialize status.memory if needed.
	if agentDeploy.Status.Memory == nil {
		agentDeploy.Status.Memory = &agentrollv1alpha1.MemoryStatus{}
	}
	ms := agentDeploy.Status.Memory

	// Take a snapshot if the interval has elapsed.
	if shouldTakeSnapshot(ms.LastSnapshotAt, intervalMinutes) && len(agentDeploy.Status.EvalHistory) > 0 {
		// Compute mean quality from the last min(10, len) eval history entries.
		sampleN := 10
		if len(agentDeploy.Status.EvalHistory) < sampleN {
			sampleN = len(agentDeploy.Status.EvalHistory)
		}
		mean := recentMeanQuality(agentDeploy.Status.EvalHistory, sampleN)

		now := metav1.Now()
		entry := agentrollv1alpha1.MemorySnapshotEntry{
			At:               now,
			CompositeVersion: compositeVersion,
			MeanQualityScore: mean,
			SampleCount:      int32(sampleN),
		}
		appendMemorySnapshot(ms, entry, maxSnapshots)
		ms.LastSnapshotAt = &now

		log.Info("Memory snapshot taken",
			"agent", agentDeploy.Name,
			"version", compositeVersion,
			"meanQuality", fmt.Sprintf("%.3f", mean),
			"snapshots", len(ms.Snapshots))
	}

	// Compute drift score and update status.
	drift := computeDriftScore(ms.Snapshots)
	ms.DriftScore = drift

	// Determine drift threshold (default 0.10 = 10% quality drop).
	threshold := 0.10
	if mem.DriftThreshold != nil {
		var parsed float64
		if _, err := fmt.Sscanf(*mem.DriftThreshold, "%f", &parsed); err == nil {
			threshold = parsed
		}
	}

	driftDetected := drift < -threshold
	ms.DriftDetected = driftDetected

	if driftDetected {
		msg := fmt.Sprintf(
			"Memory drift detected: quality dropped %.1f%% from baseline (threshold: %.1f%%)",
			-drift*100, threshold*100)
		log.Info(msg, "agent", agentDeploy.Name, "driftScore", fmt.Sprintf("%.4f", drift))

		if mem.RollbackOnDrift {
			r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "MemoryDriftDetected", msg+
				" — consider rolling back to a previous stable version.")
		} else {
			r.Recorder.Event(agentDeploy, corev1.EventTypeWarning, "MemoryDriftDetected", msg)
		}
	}

	return nil
}

// ─── 12.2 Drift score computation ────────────────────────────────────────────

// computeDriftScore computes (recentMean - baselineMean) / baselineMean from
// snapshots split into equal halves. Requires at least 4 snapshots; returns 0
// when there are fewer.
//
// Negative values indicate quality degradation; positive values indicate improvement.
func computeDriftScore(snapshots []agentrollv1alpha1.MemorySnapshotEntry) float64 {
	if len(snapshots) < 4 {
		return 0.0
	}

	half := len(snapshots) / 2
	baseline := snapshots[:half]
	recent := snapshots[len(snapshots)-half:]

	var baselineSum, recentSum float64
	for _, s := range baseline {
		baselineSum += s.MeanQualityScore
	}
	for _, s := range recent {
		recentSum += s.MeanQualityScore
	}

	baselineMean := baselineSum / float64(len(baseline))
	recentMean := recentSum / float64(len(recent))

	if baselineMean == 0 {
		return 0.0
	}
	return (recentMean - baselineMean) / baselineMean
}

// ─── 12.3 Snapshot ring buffer ───────────────────────────────────────────────

// appendMemorySnapshot appends entry to ms.Snapshots, capping at maxSnapshots.
// Oldest entries are evicted when the buffer is full.
func appendMemorySnapshot(
	ms *agentrollv1alpha1.MemoryStatus,
	entry agentrollv1alpha1.MemorySnapshotEntry,
	maxSnapshots int32,
) {
	ms.Snapshots = append(ms.Snapshots, entry)
	if int32(len(ms.Snapshots)) > maxSnapshots {
		ms.Snapshots = ms.Snapshots[int32(len(ms.Snapshots))-maxSnapshots:]
	}
}

// ─── 12.4 Interval gate ──────────────────────────────────────────────────────

// shouldTakeSnapshot returns true if lastSnapshotAt is nil (first snapshot) or
// if more than intervalMinutes have elapsed since the last snapshot.
func shouldTakeSnapshot(lastSnapshotAt *metav1.Time, intervalMinutes int32) bool {
	if lastSnapshotAt == nil {
		return true
	}
	elapsed := time.Since(lastSnapshotAt.Time)
	return elapsed >= time.Duration(intervalMinutes)*time.Minute
}

// ─── 12.5 Recent mean quality ────────────────────────────────────────────────

// recentMeanQuality computes the mean QualityScore from the last n entries of
// evalHistory. Returns 0.0 when history is empty.
func recentMeanQuality(history []agentrollv1alpha1.EvalHistoryEntry, n int) float64 {
	if len(history) == 0 {
		return 0.0
	}
	start := len(history) - n
	if start < 0 {
		start = 0
	}
	recent := history[start:]
	var sum float64
	for _, e := range recent {
		sum += e.QualityScore
	}
	return sum / float64(len(recent))
}
