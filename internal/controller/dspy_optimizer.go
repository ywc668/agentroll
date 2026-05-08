/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// dspy_optimizer.go — Sprint 13.3: DSPy-style Prompt Optimization
//
// Implements reconcileDspyOptimizer (called from reconcileEvolution when mode=dspy).
// Uses eval history as MIPRO-style training examples: formats quality scores
// for each composite version, sends to the configured LLM with an optimization
// system prompt, creates a PromptVariant CRD, and patches
// spec.evolution.promptExperiment to wire it into the existing A/B test machinery.

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// minDspySamples is the minimum number of eval history entries required before
// the DSPy optimizer runs. Ensures the LLM has enough training signal.
const minDspySamples = 5

// reconcileDspyOptimizer is called from reconcileEvolution when
// spec.evolution.optimizer.mode == "dspy".
//
// Guards:
//   - No-op when spec.evolution.promptExperiment is already set.
//   - No-op when status.evolution.dspyJobName points to a Pending/Testing variant.
//   - No-op when eval history has fewer than minDspySamples entries.
func (r *AgentDeploymentReconciler) reconcileDspyOptimizer(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	log := logf.FromContext(ctx)
	ev := agentDeploy.Spec.Evolution

	// Guard: A/B experiment already active — don't override.
	if ev.PromptExperiment != "" {
		return "", nil
	}

	// Guard: A previous DSPy variant is still Pending or Testing.
	if agentDeploy.Status.Evolution != nil && agentDeploy.Status.Evolution.DspyJobName != "" {
		prevName := agentDeploy.Status.Evolution.DspyJobName
		pv := &agentrollv1alpha1.PromptVariant{}
		getErr := r.Get(ctx, client.ObjectKey{Name: prevName, Namespace: agentDeploy.Namespace}, pv)
		if getErr == nil &&
			(pv.Status.Phase == agentrollv1alpha1.PromptVariantPhasePending ||
				pv.Status.Phase == agentrollv1alpha1.PromptVariantPhaseTesting) {
			log.Info("DSPy variant still active, skipping re-optimization",
				"variant", prevName, "phase", pv.Status.Phase)
			return "", nil
		}
		// Variant not found or finished — proceed with new optimization.
	}

	// Guard: insufficient training signal.
	history := agentDeploy.Status.EvalHistory
	if len(history) < minDspySamples {
		return "", nil
	}

	// Read the current system prompt (if a ConfigMap is configured).
	currentPrompt, err := r.readCurrentSystemPrompt(ctx, agentDeploy)
	if err != nil {
		log.Error(err, "failed to read current system prompt, proceeding without it")
	}

	// Read the LLM API key.
	apiKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, ev.Optimizer.SecretRef, "API_KEY")
	if err != nil {
		return "", fmt.Errorf("reconcileDspyOptimizer: reading API key: %w", err)
	}

	// Build MIPRO-style prompt and call LLM.
	sysPrompt := buildDspyOptimizerSystemPrompt()
	userMsg := buildDspyOptimizerUserMessage(currentPrompt, history)

	optimized, err := r.callLLM(ctx, ev.Optimizer.Provider, ev.Optimizer.Model, apiKey, sysPrompt, userMsg)
	if err != nil {
		return "", fmt.Errorf("reconcileDspyOptimizer: LLM call: %w", err)
	}
	if optimized == "" {
		return "", nil
	}

	// Create the PromptVariant CRD.
	variantName, err := r.createDspyPromptVariant(ctx, agentDeploy, optimized, currentPrompt, len(history))
	if err != nil {
		return "", fmt.Errorf("reconcileDspyOptimizer: creating PromptVariant: %w", err)
	}

	// Store in status (in-memory; persisted by the normal status update at reconcile end).
	if agentDeploy.Status.Evolution == nil {
		agentDeploy.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{}
	}
	agentDeploy.Status.Evolution.DspyJobName = variantName

	// Wire the variant into the A/B test machinery by patching spec.evolution.promptExperiment.
	specPatch := client.MergeFrom(agentDeploy.DeepCopy())
	agentDeploy.Spec.Evolution.PromptExperiment = variantName
	if patchErr := r.Patch(ctx, agentDeploy, specPatch); patchErr != nil {
		log.Error(patchErr, "failed to patch spec.evolution.promptExperiment; variant created but not auto-wired",
			"variant", variantName)
		return fmt.Sprintf("dspy-optimizer: created PromptVariant %q from %d samples (auto-wire failed — set spec.evolution.promptExperiment manually)",
			variantName, len(history)), nil
	}

	log.Info("DSPy optimizer created and wired PromptVariant",
		"variant", variantName, "samples", len(history))
	return fmt.Sprintf("dspy-optimizer: created PromptVariant %q from %d samples", variantName, len(history)), nil
}

// readCurrentSystemPrompt reads the current system prompt from the agent's PromptConfigMap.
// Returns "" when no ConfigMap is configured or the key is absent (non-fatal).
func (r *AgentDeploymentReconciler) readCurrentSystemPrompt(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	ev := agentDeploy.Spec.Evolution
	if ev == nil || ev.PromptConfigMap == "" {
		return "", nil
	}
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentDeploy.Namespace,
		Name:      ev.PromptConfigMap,
	}, cm); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("readCurrentSystemPrompt: %w", err)
	}
	return cm.Data["system_prompt"], nil
}

// buildDspyExamples formats eval history entries as MIPRO-style training examples.
func buildDspyExamples(history []agentrollv1alpha1.EvalHistoryEntry) string {
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, e := range history {
		verdict := e.Verdict
		if verdict == "" {
			if e.QualityScore >= 0.7 {
				verdict = "pass"
			} else {
				verdict = "fail"
			}
		}
		sb.WriteString(fmt.Sprintf("Example %d: version=%s quality=%.2f verdict=%s\n",
			i+1, e.CompositeVersion, e.QualityScore, verdict))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// buildDspyOptimizerSystemPrompt returns the system prompt for the LLM acting
// as a MIPRO-style prompt optimizer.
func buildDspyOptimizerSystemPrompt() string {
	return `You are an expert AI agent system prompt optimizer using a DSPy-style approach.

You will receive:
1. A list of training examples showing prompt versions with their quality scores (0.0–1.0).
2. The current system prompt (if known).

Your task:
- Analyse the quality pattern: which configurations passed vs. failed?
- Identify what the failing configurations might be doing wrong.
- Rewrite the system prompt to address those weaknesses and reinforce what works.

Rules:
- Output ONLY the new system prompt text — no explanation, no markdown, no preamble.
- Keep the prompt concise and actionable (under 600 words).
- Preserve the agent's core purpose; only improve clarity and robustness.`
}

// buildDspyOptimizerUserMessage formats the LLM request with training examples
// and the current prompt.
func buildDspyOptimizerUserMessage(currentPrompt string, history []agentrollv1alpha1.EvalHistoryEntry) string {
	var sb strings.Builder
	sb.WriteString("Training examples (composite version → quality score → verdict):\n")
	sb.WriteString(buildDspyExamples(history))
	sb.WriteString("\n\n")
	if currentPrompt != "" {
		sb.WriteString("Current system prompt:\n")
		sb.WriteString(currentPrompt)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("Current system prompt: (unknown — not configured in spec.evolution.promptConfigMap)\n\n")
	}
	sb.WriteString("Based on the training examples above, write an improved system prompt that is likely to achieve higher quality scores. Output ONLY the new prompt text.")
	return sb.String()
}

// dspyVariantName generates a unique name for a DSPy-created PromptVariant.
func dspyVariantName(agentName string, unixTimestamp int64) string {
	return fmt.Sprintf("%s-dspy-%d", agentName, unixTimestamp)
}

// createDspyPromptVariant creates a new PromptVariant CRD with the optimized prompt.
// Returns the name of the created PromptVariant.
func (r *AgentDeploymentReconciler) createDspyPromptVariant(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	optimizedPrompt, _ string,
	sampleCount int,
) (string, error) {
	name := dspyVariantName(agentDeploy.Name, time.Now().Unix())
	hypothesis := fmt.Sprintf("DSPy-style optimization from %d eval history samples — addresses quality patterns in training data", sampleCount)

	parentVersion := agentDeploy.Spec.AgentMeta.PromptVersion
	if parentVersion == "" {
		parentVersion = "default"
	}

	pv := &agentrollv1alpha1.PromptVariant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: agentDeploy.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":    "agentroll",
				"agentroll.dev/optimization-type": "dspy",
				"agentroll.dev/agent":             agentDeploy.Name,
			},
		},
		Spec: agentrollv1alpha1.PromptVariantSpec{
			AgentDeploymentRef: agentDeploy.Name,
			SystemPrompt:       optimizedPrompt,
			ParentVersion:      parentVersion,
			Hypothesis:         hypothesis,
		},
	}

	if err := r.Create(ctx, pv); err != nil {
		return "", fmt.Errorf("createDspyPromptVariant: create PromptVariant %q: %w", name, err)
	}
	return name, nil
}
