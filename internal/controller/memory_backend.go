/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// memory_backend.go — Sprint 12b: Real Memory Backend Integration
//
// Implements:
//   reconcileMemoryBackend (Step 5.12) — phase-transition snapshot/restore.
//   takeMemorySnapshot     — export from Mem0 API; store as a K8s Secret.
//   restoreMemorySnapshot  — read K8s Secret; import to Mem0 API.
//   readBackendAPIKey      — read MEM0_API_KEY from a K8s Secret.
//   mem0ExportMemory       — GET /api/memory/export.
//   mem0ImportMemory       — POST /api/memory/import.
//   memorySnapshotSecretName — deterministic Secret name for a given agent.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// memorySnapshotSecretName returns the name of the K8s Secret used to store
// the pre-canary memory snapshot for agentName.
func memorySnapshotSecretName(agentName string) string {
	return agentName + "-memory-snapshot"
}

// ─── Phase-transition snapshot/restore ───────────────────────────────────────

// reconcileMemoryBackend is Step 5.12 in the reconcile loop.
// Detects when a new canary starts (stable→progressing) to snapshot memory,
// and when a canary fails (progressing→degraded/rollingBack) to restore it.
// No-op when spec.memory.backend is nil. Non-fatal.
func (r *AgentDeploymentReconciler) reconcileMemoryBackend(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	if agentDeploy.Spec.Memory == nil || agentDeploy.Spec.Memory.Backend == nil {
		return nil
	}
	if !agentDeploy.Spec.Memory.SnapshotOnPromotion && !agentDeploy.Spec.Memory.RollbackMemoryOnCanaryFail {
		return nil
	}
	log := logf.FromContext(ctx)

	// Fetch the live Rollout to determine current phase.
	rollout := &rolloutsv1alpha1.Rollout{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      agentDeploy.Name,
		Namespace: agentDeploy.Namespace,
	}, rollout); err != nil {
		if errors.IsNotFound(err) {
			return nil // Rollout not yet created — nothing to do
		}
		return fmt.Errorf("reconcileMemoryBackend: failed to get Rollout: %w", err)
	}

	livePhase := mapRolloutPhase(rollout)
	prevPhase := agentDeploy.Status.Phase

	// Snapshot: stable/pending → progressing (new canary starting).
	if agentDeploy.Spec.Memory.SnapshotOnPromotion &&
		prevPhase != agentrollv1alpha1.PhaseProgressing &&
		livePhase == agentrollv1alpha1.PhaseProgressing {
		log.Info("Canary started — taking pre-canary memory snapshot",
			"agent", agentDeploy.Name,
			"prevPhase", prevPhase,
			"livePhase", livePhase)
		if err := r.takeMemorySnapshot(ctx, agentDeploy, compositeVersion); err != nil {
			return fmt.Errorf("reconcileMemoryBackend: %w", err)
		}
	}

	// Restore: progressing → degraded/rollingBack (canary failed).
	if agentDeploy.Spec.Memory.RollbackMemoryOnCanaryFail &&
		prevPhase == agentrollv1alpha1.PhaseProgressing &&
		(livePhase == agentrollv1alpha1.PhaseDegraded || livePhase == agentrollv1alpha1.PhaseRollingBack) {
		log.Info("Canary failed — restoring pre-canary memory snapshot",
			"agent", agentDeploy.Name,
			"prevPhase", prevPhase,
			"livePhase", livePhase)
		if err := r.restoreMemorySnapshot(ctx, agentDeploy); err != nil {
			return fmt.Errorf("reconcileMemoryBackend: %w", err)
		}
	}

	return nil
}

// ─── Snapshot (export → K8s Secret) ──────────────────────────────────────────

// takeMemorySnapshot exports memory from the Mem0 backend and stores it as a
// Kubernetes Secret named "<agentName>-memory-snapshot".
func (r *AgentDeploymentReconciler) takeMemorySnapshot(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	compositeVersion string,
) error {
	backend := agentDeploy.Spec.Memory.Backend
	apiKey, err := r.readBackendAPIKey(ctx, agentDeploy.Namespace, backend.SecretRef)
	if err != nil {
		return fmt.Errorf("takeMemorySnapshot: %w", err)
	}

	data, err := mem0ExportMemory(ctx, backend.Endpoint, backend.SessionID, apiKey)
	if err != nil {
		return fmt.Errorf("takeMemorySnapshot: export failed: %w", err)
	}

	secretName := memorySnapshotSecretName(agentDeploy.Name)
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: agentDeploy.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":   "agentroll",
				"agentroll.dev/agent":            agentDeploy.Name,
				"agentroll.dev/snapshot-version": compositeVersion,
			},
		},
		Data: map[string][]byte{
			"snapshot.json": data,
		},
	}

	existing := &corev1.Secret{}
	err = r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: agentDeploy.Namespace}, existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil {
			return fmt.Errorf("takeMemorySnapshot: create secret: %w", createErr)
		}
	} else if err != nil {
		return fmt.Errorf("takeMemorySnapshot: get secret: %w", err)
	} else {
		existing.Data = desired.Data
		existing.Labels = desired.Labels
		if updateErr := r.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("takeMemorySnapshot: update secret: %w", updateErr)
		}
	}

	now := metav1.Now()
	if agentDeploy.Status.Memory == nil {
		agentDeploy.Status.Memory = &agentrollv1alpha1.MemoryStatus{}
	}
	agentDeploy.Status.Memory.LastMemorySnapshotAt = &now
	agentDeploy.Status.Memory.LastMemorySnapshotSecret = secretName
	agentDeploy.Status.Memory.LastMemorySnapshotVersion = compositeVersion

	logf.FromContext(ctx).Info("Memory snapshot stored",
		"agent", agentDeploy.Name,
		"secret", secretName,
		"version", compositeVersion,
		"bytes", len(data))
	return nil
}

// ─── Restore (K8s Secret → import) ───────────────────────────────────────────

// restoreMemorySnapshot reads the stored snapshot Secret and imports it to the
// Mem0 backend. No-op when no snapshot has been taken yet.
func (r *AgentDeploymentReconciler) restoreMemorySnapshot(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	log := logf.FromContext(ctx)
	if agentDeploy.Status.Memory == nil || agentDeploy.Status.Memory.LastMemorySnapshotSecret == "" {
		log.Info("No memory snapshot to restore", "agent", agentDeploy.Name)
		return nil
	}

	secretName := agentDeploy.Status.Memory.LastMemorySnapshotSecret
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: agentDeploy.Namespace,
	}, secret); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Memory snapshot secret not found — skipping restore", "secret", secretName)
			return nil
		}
		return fmt.Errorf("restoreMemorySnapshot: get secret: %w", err)
	}

	data, ok := secret.Data["snapshot.json"]
	if !ok {
		return fmt.Errorf("restoreMemorySnapshot: snapshot.json key missing from secret %s", secretName)
	}

	backend := agentDeploy.Spec.Memory.Backend
	apiKey, err := r.readBackendAPIKey(ctx, agentDeploy.Namespace, backend.SecretRef)
	if err != nil {
		return fmt.Errorf("restoreMemorySnapshot: %w", err)
	}

	if err := mem0ImportMemory(ctx, backend.Endpoint, backend.SessionID, apiKey, data); err != nil {
		return fmt.Errorf("restoreMemorySnapshot: import failed: %w", err)
	}

	log.Info("Memory snapshot restored",
		"agent", agentDeploy.Name,
		"secret", secretName,
		"version", agentDeploy.Status.Memory.LastMemorySnapshotVersion)
	return nil
}

// ─── Credentials helper ───────────────────────────────────────────────────────

// readBackendAPIKey reads the MEM0_API_KEY value from the named Kubernetes Secret.
func (r *AgentDeploymentReconciler) readBackendAPIKey(
	ctx context.Context,
	namespace, secretRef string,
) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      secretRef,
		Namespace: namespace,
	}, secret); err != nil {
		return "", fmt.Errorf("readBackendAPIKey: get secret %q: %w", secretRef, err)
	}
	key, ok := secret.Data["MEM0_API_KEY"]
	if !ok {
		return "", fmt.Errorf("readBackendAPIKey: secret %q missing MEM0_API_KEY", secretRef)
	}
	return string(key), nil
}

// ─── Mem0 HTTP client ─────────────────────────────────────────────────────────

// mem0ExportMemory calls GET <endpoint>/api/memory/export?session_id=<sessionID>
// with Bearer authorization and returns the raw response body.
func mem0ExportMemory(ctx context.Context, endpoint, sessionID, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/memory/export", nil)
	if err != nil {
		return nil, fmt.Errorf("mem0ExportMemory: create request: %w", err)
	}
	if sessionID != "" {
		q := req.URL.Query()
		q.Set("session_id", sessionID)
		req.URL.RawQuery = q.Encode()
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mem0ExportMemory: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mem0ExportMemory: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mem0ExportMemory: unexpected status %d: %s",
			resp.StatusCode, string(body))
	}
	return body, nil
}

// mem0ImportMemory calls POST <endpoint>/api/memory/import?session_id=<sessionID>
// with data as the JSON request body. Returns an error on non-2xx status.
func mem0ImportMemory(ctx context.Context, endpoint, sessionID, apiKey string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint+"/api/memory/import", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("mem0ImportMemory: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		q := req.URL.Query()
		q.Set("session_id", sessionID)
		req.URL.RawQuery = q.Encode()
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mem0ImportMemory: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mem0ImportMemory: unexpected status %d: %s",
			resp.StatusCode, string(body))
	}
	return nil
}
