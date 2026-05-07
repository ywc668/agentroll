/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

// evolution.go — Sprint 7: Self-Evolution Loop
//
// This file implements the three evolution strategies:
//
//   7.2  Threshold Tuner    — reads historical AnalysisRun outcomes, computes rolling
//                             statistics, and adjusts quality-gate thresholds with no LLM.
//   7.3  Prompt Optimizer   — reads failing Langfuse traces, calls an LLM (Anthropic/OpenAI),
//                             and opens a GitHub PR with suggested prompt improvements.
//   7.4  Model Upgrader     — detects quality plateaus across N consecutive successful canaries
//                             and proposes a model version bump via GitHub PR.
//
// All LLM and GitHub API calls are made using stdlib net/http — no external SDK is needed.
// Context-aware timeouts prevent reconcile loops from blocking indefinitely.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// reconcileEvolution is Step 5.7 in the reconcile loop.
// It evaluates whether the self-evolution loop should fire and dispatches to
// the configured strategies (threshold tuner, prompt optimizer, model upgrader).
func (r *AgentDeploymentReconciler) reconcileEvolution(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) error {
	ev := agentDeploy.Spec.Evolution
	if ev == nil || !ev.Enabled {
		return nil
	}

	log := logf.FromContext(ctx)
	phase := agentDeploy.Status.Phase

	// Determine whether to fire the evolution loop.
	shouldFire := false
	switch ev.Trigger {
	case "on-canary-fail", "":
		shouldFire = (phase == agentrollv1alpha1.PhaseDegraded || phase == agentrollv1alpha1.PhaseRollingBack)
	case "periodic":
		shouldFire = r.periodicTriggerDue(agentDeploy)
	case "both":
		shouldFire = (phase == agentrollv1alpha1.PhaseDegraded || phase == agentrollv1alpha1.PhaseRollingBack) ||
			r.periodicTriggerDue(agentDeploy)
	}

	if !shouldFire {
		return nil
	}

	log.Info("Evolution loop firing", "name", agentDeploy.Name,
		"strategy", ev.Strategy, "trigger", ev.Trigger, "phase", phase)

	strategy := ev.Strategy
	if strategy == "" {
		strategy = "all"
	}

	runThreshold := strategy == "threshold-tuner" || strategy == "all"
	runPrompt := strategy == "prompt-optimizer" || strategy == "all"
	runModel := strategy == "model-upgrader" || strategy == "all"

	var proposals []string

	// 7.2 Threshold Tuner — no LLM required.
	if runThreshold {
		proposal, err := r.runThresholdTuner(ctx, agentDeploy)
		if err != nil {
			log.Error(err, "threshold tuner failed, continuing with other strategies")
		} else if proposal != "" {
			proposals = append(proposals, proposal)
		}
	}

	// 7.3 Prompt Optimizer — requires LLM and fires on failure.
	if runPrompt && (phase == agentrollv1alpha1.PhaseDegraded || phase == agentrollv1alpha1.PhaseRollingBack) {
		if ev.Optimizer != nil {
			proposal, err := r.runPromptOptimizer(ctx, agentDeploy)
			if err != nil {
				log.Error(err, "prompt optimizer failed, continuing")
			} else if proposal != "" {
				proposals = append(proposals, proposal)
			}
		} else {
			log.Info("Prompt optimizer skipped: spec.evolution.optimizer not configured")
		}
	}

	// 7.4 Model Upgrader — fires when quality plateaus across N canaries.
	if runModel {
		if ev.Optimizer != nil {
			proposal, err := r.runModelUpgrader(ctx, agentDeploy)
			if err != nil {
				log.Error(err, "model upgrader failed, continuing")
			} else if proposal != "" {
				proposals = append(proposals, proposal)
			}
		} else {
			log.Info("Model upgrader skipped: spec.evolution.optimizer not configured")
		}
	}

	if len(proposals) == 0 {
		return nil
	}

	// Update evolution status.
	summary := strings.Join(proposals, "; ")
	now := metav1.Now()
	if agentDeploy.Status.Evolution == nil {
		agentDeploy.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{}
	}
	agentDeploy.Status.Evolution.LastProposal = summary
	agentDeploy.Status.Evolution.LastProposalAt = &now
	agentDeploy.Status.Evolution.ProposalCount++

	// Append one history entry per strategy that produced a proposal.
	for _, p := range proposals {
		strategyName := "unknown"
		if idx := strings.Index(p, ":"); idx > 0 {
			strategyName = p[:idx]
		}
		appendEvolutionHistory(agentDeploy.Status.Evolution,
			agentrollv1alpha1.EvolutionHistoryEntry{
				At:          now,
				Strategy:    strategyName,
				Description: p,
				Phase:       string(phase),
			})
	}

	log.Info("Evolution proposals generated", "count", len(proposals), "summary", summary)
	return nil
}

// appendEvolutionHistory appends entry to the history ring buffer, trimming to the
// last maxEvolutionHistory entries so status does not grow without bound.
func appendEvolutionHistory(st *agentrollv1alpha1.EvolutionStatus, entry agentrollv1alpha1.EvolutionHistoryEntry) {
	const maxEvolutionHistory = 20
	st.History = append(st.History, entry)
	if len(st.History) > maxEvolutionHistory {
		st.History = st.History[len(st.History)-maxEvolutionHistory:]
	}
}

// periodicTriggerDue returns true when the periodic schedule is due.
// Currently uses a simple "once per day" heuristic; a full cron parser
// can be added when the Schedule field is wired up.
func (r *AgentDeploymentReconciler) periodicTriggerDue(
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) bool {
	ev := agentDeploy.Spec.Evolution
	if ev == nil || ev.Schedule == "" {
		return false
	}
	st := agentDeploy.Status.Evolution
	if st == nil || st.NextEvalAt == nil {
		return true // never run yet
	}
	return time.Now().After(st.NextEvalAt.Time)
}

// ─── 7.2 Threshold Tuner ─────────────────────────────────────────────────────

// runThresholdTuner lists all completed AnalysisRuns for this agent, extracts
// numeric metric measurements, computes rolling statistics, and stores adjusted
// thresholds in status.evolution.tunedThresholds.
//
// Adjustment rules:
//   - Metrics that represent minimums (quality scores, tool success rates):
//     new threshold = mean − 1.5 * stddev  (loosen slightly so passing canaries aren't over-constrained)
//   - Metrics that represent maximums (latency, cost, token count):
//     new threshold = mean + 1.5 * stddev  (allow normal variance while blocking outliers)
//
// The tuner infers direction from the metric name:
//   - names containing "latency", "cost", "token", "error" → upper bound
//   - everything else → lower bound (quality metric)
func (r *AgentDeploymentReconciler) runThresholdTuner(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	log := logf.FromContext(ctx)

	// List AnalysisRuns owned by the Rollout for this agent.
	analysisRuns := &rolloutsv1alpha1.AnalysisRunList{}
	if err := r.List(ctx, analysisRuns,
		client.InNamespace(agentDeploy.Namespace),
		client.MatchingLabels{
			"rollouts.argoproj.io/rollout": agentDeploy.Name,
		},
	); err != nil {
		return "", fmt.Errorf("listing AnalysisRuns: %w", err)
	}

	if len(analysisRuns.Items) == 0 {
		log.Info("No AnalysisRuns found yet, threshold tuner skipped")
		return "", nil
	}

	// Collect metric values per metric name across all successful AnalysisRuns.
	metricValues := map[string][]float64{}

	for i := range analysisRuns.Items {
		ar := &analysisRuns.Items[i]
		if ar.Status.Phase != rolloutsv1alpha1.AnalysisPhaseSuccessful {
			continue // only learn from passing runs
		}
		for _, mr := range ar.Status.MetricResults {
			for _, m := range mr.Measurements {
				if m.Value == "" {
					continue
				}
				v, err := strconv.ParseFloat(m.Value, 64)
				if err != nil {
					continue // non-numeric measurement (e.g., JSON blob)
				}
				metricValues[mr.Name] = append(metricValues[mr.Name], v)
			}
		}
	}

	// Job-based AnalysisRuns produce no numeric measurements (only exit codes).
	// Fall back to Langfuse scores as the numeric signal when Langfuse is configured.
	if len(metricValues) == 0 {
		if obs := agentDeploy.Spec.Observability; obs != nil && obs.Langfuse != nil {
			langfuseScores, err := r.fetchLangfuseScores(ctx, agentDeploy, obs.Langfuse)
			if err != nil {
				log.Error(err, "failed to fetch Langfuse scores for threshold tuner, skipping")
			} else {
				metricValues = langfuseScores
			}
		}
	}

	if len(metricValues) == 0 {
		log.Info("No numeric metric values found (AnalysisRuns or Langfuse), threshold tuner skipped")
		return "", nil
	}

	// Require at least 3 data points before proposing a threshold adjustment
	// to avoid noise from a single outlier canary.
	const minDataPoints = 3

	tuned := map[string]string{}
	var adjustedMetrics []string

	for name, vals := range metricValues {
		if len(vals) < minDataPoints {
			continue
		}
		mean, stddev := computeStats(vals)
		upperBound := isUpperBoundMetric(name)

		var newThreshold float64
		if upperBound {
			newThreshold = mean + 1.5*stddev
		} else {
			newThreshold = mean - 1.5*stddev
			if newThreshold < 0 {
				newThreshold = 0
			}
		}

		tuned[name] = strconv.FormatFloat(newThreshold, 'f', 4, 64)
		adjustedMetrics = append(adjustedMetrics, fmt.Sprintf("%s→%s", name, tuned[name]))
	}

	if len(tuned) == 0 {
		return "", nil
	}

	// Persist tuned thresholds into status.
	if agentDeploy.Status.Evolution == nil {
		agentDeploy.Status.Evolution = &agentrollv1alpha1.EvolutionStatus{}
	}
	if agentDeploy.Status.Evolution.TunedThresholds == nil {
		agentDeploy.Status.Evolution.TunedThresholds = map[string]string{}
	}
	for k, v := range tuned {
		agentDeploy.Status.Evolution.TunedThresholds[k] = v
	}

	sort.Strings(adjustedMetrics)
	proposal := fmt.Sprintf("threshold-tuner: adjusted %s", strings.Join(adjustedMetrics, ", "))
	log.Info("Threshold tuner completed", "adjustedMetrics", adjustedMetrics)
	return proposal, nil
}

// isUpperBoundMetric returns true when lower values are better (so we set an upper bound).
func isUpperBoundMetric(name string) bool {
	n := strings.ToLower(name)
	for _, kw := range []string{"latency", "cost", "token", "error", "fail"} {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

// computeStats returns the mean and population standard deviation of the slice.
func computeStats(vals []float64) (mean, stddev float64) {
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	mean = sum / float64(len(vals))
	varSum := 0.0
	for _, v := range vals {
		d := v - mean
		varSum += d * d
	}
	stddev = math.Sqrt(varSum / float64(len(vals)))
	return
}

// ─── 7.3 Prompt Optimizer ────────────────────────────────────────────────────

// runPromptOptimizer reads the most recent failed AnalysisRun for this agent,
// optionally enriches the context with Langfuse trace data, and asks the
// configured LLM to suggest prompt improvements. The suggestion is either
// opened as a GitHub PR (if humanApproval is configured) or stored in status.
func (r *AgentDeploymentReconciler) runPromptOptimizer(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	log := logf.FromContext(ctx)
	ev := agentDeploy.Spec.Evolution

	// Find the most recent failed AnalysisRun.
	failureContext, err := r.buildFailureContext(ctx, agentDeploy)
	if err != nil {
		return "", fmt.Errorf("building failure context: %w", err)
	}
	if failureContext == "" {
		log.Info("No failure context found, prompt optimizer skipped")
		return "", nil
	}

	// Optionally enrich with Langfuse trace data.
	if obs := agentDeploy.Spec.Observability; obs != nil && obs.Langfuse != nil {
		traces, err := r.fetchLangfuseFailingTraces(ctx, agentDeploy, obs.Langfuse)
		if err != nil {
			log.Error(err, "failed to fetch Langfuse traces, proceeding without them")
		} else if traces != "" {
			failureContext += "\n\nLangfuse trace samples from the failed canary:\n" + traces
		}
	}

	// Read the LLM API key from the referenced Secret.
	apiKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, ev.Optimizer.SecretRef, "API_KEY")
	if err != nil {
		return "", fmt.Errorf("reading optimizer API key: %w", err)
	}

	// Build the LLM prompt.
	systemPrompt := `You are an expert AI agent prompt engineer.
Analyse the following failure context from a canary deployment of an AI agent and suggest
specific, actionable improvements to the agent's system prompt.

Focus on:
1. Reducing hallucinations or incorrect tool usage
2. Improving response quality and relevance
3. Better handling of edge cases that caused failures

Provide concrete rewritten prompt fragments, not vague suggestions.
Keep your response concise (under 500 words) and structured.`

	userMessage := "Failure context from the failed canary deployment:\n\n" + failureContext

	suggestion, err := r.callLLM(ctx, ev.Optimizer.Provider, ev.Optimizer.Model, apiKey,
		systemPrompt, userMessage)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	log.Info("Prompt optimizer generated suggestion",
		"model", ev.Optimizer.Model,
		"suggestionLength", len(suggestion))

	// When humanApproval is configured, open a GitHub PR for human review.
	if ev.HumanApproval != nil && ev.HumanApproval.GitHub != nil {
		prURL, err := r.openEvolutionPR(ctx, agentDeploy, "prompt-optimizer", suggestion)
		if err != nil {
			log.Error(err, "failed to open GitHub PR for prompt optimizer")
			return fmt.Sprintf("prompt-optimizer: suggestion generated (PR failed: %v)", err), nil
		}
		return fmt.Sprintf("prompt-optimizer: PR opened %s", prURL), nil
	}

	// When promptConfigMap is configured (and no human approval gate), write the
	// improved prompt directly to the ConfigMap so the next pod restart picks it up.
	if ev.PromptConfigMap != "" {
		if err := r.writePromptToConfigMap(ctx, agentDeploy, suggestion); err != nil {
			log.Error(err, "failed to write prompt to ConfigMap",
				"configmap", ev.PromptConfigMap)
			return fmt.Sprintf("prompt-optimizer: suggestion generated (ConfigMap write failed: %v)", err), nil
		}
		log.Info("Prompt optimizer wrote improved prompt to ConfigMap",
			"configmap", ev.PromptConfigMap)
		return fmt.Sprintf("prompt-optimizer: prompt written to ConfigMap %s (%d chars)",
			ev.PromptConfigMap, len(suggestion)), nil
	}

	return fmt.Sprintf("prompt-optimizer: suggestion generated (%d chars)", len(suggestion)), nil
}

// writePromptToConfigMap creates or updates the named ConfigMap with the new system prompt
// under the key "system_prompt". The ConfigMap is owned by the AgentDeployment so it is
// garbage-collected when the AgentDeployment is deleted.
func (r *AgentDeploymentReconciler) writePromptToConfigMap(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	prompt string,
) error {
	cm := &corev1.ConfigMap{}
	name := agentDeploy.Spec.Evolution.PromptConfigMap
	key := client.ObjectKey{Namespace: agentDeploy.Namespace, Name: name}

	if err := r.Get(ctx, key, cm); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("get ConfigMap %s: %w", name, err)
		}
		// ConfigMap does not exist yet — create it.
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: agentDeploy.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "agentroll",
					"agentroll.dev/component":      "prompt-store",
				},
			},
			Data: map[string]string{"system_prompt": prompt},
		}
		return r.Create(ctx, cm)
	}

	// ConfigMap exists — update the system_prompt key.
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["system_prompt"] = prompt
	return r.Update(ctx, cm)
}

// buildFailureContext extracts a textual summary of what failed in the most recent
// failed AnalysisRun for this agent.
func (r *AgentDeploymentReconciler) buildFailureContext(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	analysisRuns := &rolloutsv1alpha1.AnalysisRunList{}
	if err := r.List(ctx, analysisRuns,
		client.InNamespace(agentDeploy.Namespace),
		client.MatchingLabels{
			"rollouts.argoproj.io/rollout": agentDeploy.Name,
		},
	); err != nil {
		return "", fmt.Errorf("listing AnalysisRuns: %w", err)
	}

	// Find the most recent failed run.
	var latestFailed *rolloutsv1alpha1.AnalysisRun
	for i := range analysisRuns.Items {
		ar := &analysisRuns.Items[i]
		if ar.Status.Phase != rolloutsv1alpha1.AnalysisPhaseFailed {
			continue
		}
		if latestFailed == nil || ar.CreationTimestamp.After(latestFailed.CreationTimestamp.Time) {
			latestFailed = ar
		}
	}
	if latestFailed == nil {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Agent: %s\n", agentDeploy.Name))
	sb.WriteString(fmt.Sprintf("Canary version: %s\n", agentDeploy.Status.CanaryVersion))
	sb.WriteString(fmt.Sprintf("AnalysisRun: %s\n", latestFailed.Name))
	sb.WriteString("Failed metrics:\n")

	for _, mr := range latestFailed.Status.MetricResults {
		if mr.Phase != rolloutsv1alpha1.AnalysisPhaseFailed {
			continue
		}
		sb.WriteString(fmt.Sprintf("  - %s: phase=%s", mr.Name, mr.Phase))
		if len(mr.Measurements) > 0 {
			last := mr.Measurements[len(mr.Measurements)-1]
			if last.Value != "" {
				sb.WriteString(fmt.Sprintf(", measured=%s", last.Value))
			}
			if last.Message != "" {
				sb.WriteString(fmt.Sprintf(", message=%s", last.Message))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// fetchLangfuseFailingTraces queries the Langfuse API for traces tagged with
// the current canary version and returns a summarised text sample.
func (r *AgentDeploymentReconciler) fetchLangfuseFailingTraces(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) (string, error) {
	if lf.SecretRef == "" {
		return "", nil
	}

	publicKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_PUBLIC_KEY")
	if err != nil {
		return "", err
	}
	secretKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_SECRET_KEY")
	if err != nil {
		return "", err
	}

	host := lf.Endpoint
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	host = strings.TrimRight(host, "/")

	canaryVersion := agentDeploy.Status.CanaryVersion

	reqURL := fmt.Sprintf("%s/api/public/traces?tags=canary:%s&limit=5", host, canaryVersion)
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(publicKey, secretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Langfuse API %s returned %d: %s", reqURL, resp.StatusCode, body)
	}

	// Parse just enough to extract trace IDs and output snippets.
	var result struct {
		Data []struct {
			ID     string   `json:"id"`
			Output string   `json:"output"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	if len(result.Data) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for i, trace := range result.Data {
		sb.WriteString(fmt.Sprintf("Trace %d (id=%s):\n", i+1, trace.ID))
		if trace.Output != "" {
			out := trace.Output
			if len(out) > 300 {
				out = out[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("  output: %s\n", out))
		}
	}
	return sb.String(), nil
}

// fetchLangfuseScores queries the Langfuse Scores API and returns a map of
// score name → float64 slice usable by the threshold tuner.
//
// Score name normalisation (Langfuse name → threshold key):
//
//	avg_latency        → max_latency_ms       (upper bound — lower is better)
//	tool_success_rate  → min_success_rate      (lower bound — higher is better)
//	hallucination_rate → max_hallucination_rate (upper bound — lower is better)
//
// Other score names are used as-is so user-defined scores also benefit from tuning.
func (r *AgentDeploymentReconciler) fetchLangfuseScores(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	lf *agentrollv1alpha1.LangfuseSpec,
) (map[string][]float64, error) {
	if lf.SecretRef == "" {
		return nil, nil
	}

	publicKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_PUBLIC_KEY")
	if err != nil {
		return nil, err
	}
	secretKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, lf.SecretRef, "LANGFUSE_SECRET_KEY")
	if err != nil {
		return nil, err
	}

	host := lf.Endpoint
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	host = strings.TrimRight(host, "/")

	// Query the last 100 scores across all traces for this agent.
	// Langfuse scores are tagged with the agent name via the trace name prefix.
	reqURL := fmt.Sprintf("%s/api/public/scores?limit=100", host)
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(publicKey, secretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Langfuse scores API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing Langfuse scores response: %w", err)
	}

	out := map[string][]float64{}
	for _, s := range result.Data {
		key := normalizeLangfuseScoreName(s.Name)
		out[key] = append(out[key], s.Value)
	}
	return out, nil
}

// normalizeLangfuseScoreName converts a Langfuse score name to its threshold key.
// Used by the threshold tuner and tool check template.
func normalizeLangfuseScoreName(name string) string {
	switch name {
	case "avg_latency":
		return "max_latency_ms"
	case "tool_success_rate":
		return "min_success_rate"
	case "hallucination_rate":
		return "max_hallucination_rate"
	default:
		if strings.HasPrefix(name, "tool_success_rate_") {
			// e.g. tool_success_rate_kubectl_get → min_tool_success_rate_kubectl_get
			return "min_" + name
		}
		return name
	}
}

// ─── 7.4 Model Upgrader ──────────────────────────────────────────────────────

// runModelUpgrader checks whether quality metrics have plateaued across the
// last N consecutive successful canaries. If a plateau is detected, it asks
// the configured LLM to suggest a model upgrade and opens a GitHub PR.
func (r *AgentDeploymentReconciler) runModelUpgrader(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
) (string, error) {
	log := logf.FromContext(ctx)
	ev := agentDeploy.Spec.Evolution

	plateauN := int32(3)
	if ev.ConsecutiveCanariesForPlateau != nil {
		plateauN = *ev.ConsecutiveCanariesForPlateau
	}

	plateau, avgQuality, err := r.detectQualityPlateau(ctx, agentDeploy, int(plateauN))
	if err != nil {
		return "", fmt.Errorf("detecting quality plateau: %w", err)
	}
	if !plateau {
		return "", nil
	}

	log.Info("Quality plateau detected, running model upgrader",
		"consecutiveCanaries", plateauN,
		"avgQuality", avgQuality,
	)

	currentModel := agentDeploy.Spec.AgentMeta.ModelVersion
	systemPrompt := `You are an expert AI infrastructure engineer.
An AI agent has been deployed with the same quality metrics for several consecutive canary releases,
indicating that performance has plateaued with the current model.

Suggest a specific, actionable model upgrade. Consider:
1. What newer model versions of the same provider might improve quality
2. Alternative model providers that could better serve the use case
3. Model-specific configuration changes (temperature, context window, etc.)

Be concrete and specific. Keep your response under 300 words.`

	userMessage := fmt.Sprintf(
		"Agent: %s\nCurrent model: %s\nQuality plateau detected after %d consecutive canaries.\nAverage quality score: %.4f\n\nWhat model upgrade would you recommend?",
		agentDeploy.Name, currentModel, plateauN, avgQuality,
	)

	apiKey, err := r.readSecretKey(ctx, agentDeploy.Namespace, ev.Optimizer.SecretRef, "API_KEY")
	if err != nil {
		return "", fmt.Errorf("reading optimizer API key: %w", err)
	}

	suggestion, err := r.callLLM(ctx, ev.Optimizer.Provider, ev.Optimizer.Model, apiKey,
		systemPrompt, userMessage)
	if err != nil {
		return "", fmt.Errorf("LLM call for model upgrade: %w", err)
	}

	log.Info("Model upgrader generated suggestion", "model", ev.Optimizer.Model)

	if ev.HumanApproval != nil && ev.HumanApproval.GitHub != nil {
		prURL, err := r.openEvolutionPR(ctx, agentDeploy, "model-upgrader", suggestion)
		if err != nil {
			log.Error(err, "failed to open GitHub PR for model upgrader")
			return fmt.Sprintf("model-upgrader: suggestion generated (PR failed: %v)", err), nil
		}
		return fmt.Sprintf("model-upgrader: PR opened %s", prURL), nil
	}

	return fmt.Sprintf("model-upgrader: plateau detected after %d canaries, suggestion generated", plateauN), nil
}

// detectQualityPlateau returns true when the last N completed successful AnalysisRuns
// show no improvement (stddev < 5% of mean) in quality metrics.
func (r *AgentDeploymentReconciler) detectQualityPlateau(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	n int,
) (plateau bool, avgQuality float64, err error) {
	analysisRuns := &rolloutsv1alpha1.AnalysisRunList{}
	if err = r.List(ctx, analysisRuns,
		client.InNamespace(agentDeploy.Namespace),
		client.MatchingLabels{
			"rollouts.argoproj.io/rollout": agentDeploy.Name,
		},
	); err != nil {
		return false, 0, fmt.Errorf("listing AnalysisRuns: %w", err)
	}

	// Filter to completed successful runs and sort by creation time (newest first).
	var successful []*rolloutsv1alpha1.AnalysisRun
	for i := range analysisRuns.Items {
		ar := &analysisRuns.Items[i]
		if ar.Status.Phase == rolloutsv1alpha1.AnalysisPhaseSuccessful {
			successful = append(successful, ar)
		}
	}
	sort.Slice(successful, func(i, j int) bool {
		return successful[i].CreationTimestamp.After(successful[j].CreationTimestamp.Time)
	})

	if len(successful) < n {
		return false, 0, nil // not enough history
	}

	// Take the last N successful runs and compute average quality.
	var qualityScores []float64
	for _, ar := range successful[:n] {
		for _, mr := range ar.Status.MetricResults {
			if isUpperBoundMetric(mr.Name) {
				continue // skip latency/cost metrics
			}
			for _, m := range mr.Measurements {
				if v, e := strconv.ParseFloat(m.Value, 64); e == nil {
					qualityScores = append(qualityScores, v)
				}
			}
		}
	}

	if len(qualityScores) < n {
		return false, 0, nil // insufficient quality data
	}

	mean, stddev := computeStats(qualityScores)
	avgQuality = mean

	// Plateau = stddev is less than 5% of mean (very little variation = no improvement).
	if mean == 0 {
		return false, 0, nil
	}
	coeffOfVariation := stddev / mean
	return coeffOfVariation < 0.05, avgQuality, nil
}

// ─── Shared helpers ──────────────────────────────────────────────────────────

// readSecretKey reads a single key from a Kubernetes Secret.
func (r *AgentDeploymentReconciler) readSecretKey(
	ctx context.Context,
	namespace, secretName, key string,
) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return "", fmt.Errorf("secret %s/%s not found: %w", namespace, secretName, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, secretName)
	}
	return strings.TrimSpace(string(val)), nil
}

// callLLM makes a single synchronous call to an LLM provider.
// Supports "anthropic" and "openai" providers.
// Uses a 30-second timeout to prevent reconcile loops from blocking.
func (r *AgentDeploymentReconciler) callLLM(
	ctx context.Context,
	provider, model, apiKey string,
	systemPrompt, userMessage string,
) (string, error) {
	llmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if provider == "" {
		provider = "anthropic"
	}

	switch provider {
	case "anthropic":
		return callAnthropic(llmCtx, model, apiKey, systemPrompt, userMessage)
	case "openai":
		return callOpenAI(llmCtx, model, apiKey, systemPrompt, userMessage)
	default:
		return "", fmt.Errorf("unsupported LLM provider: %q (supported: anthropic, openai)", provider)
	}
}

// callAnthropic calls the Anthropic Messages API.
func callAnthropic(ctx context.Context, model, apiKey, systemPrompt, userMessage string) (string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": 1024,
		"system":     systemPrompt,
		"messages": []message{
			{Role: "user", Content: userMessage},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic API returned %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing Anthropic response: %w", err)
	}
	for _, c := range result.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in Anthropic response")
}

// callOpenAI calls the OpenAI Chat Completions API.
func callOpenAI(ctx context.Context, model, apiKey, systemPrompt, userMessage string) (string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := map[string]interface{}{
		"model": model,
		"messages": []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		"max_tokens": 1024,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenAI API returned %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing OpenAI response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}
	return result.Choices[0].Message.Content, nil
}

// openEvolutionPR creates a branch, writes the suggestion as a Markdown file,
// and opens a pull request in the configured GitHub repository.
func (r *AgentDeploymentReconciler) openEvolutionPR(
	ctx context.Context,
	agentDeploy *agentrollv1alpha1.AgentDeployment,
	strategy, content string,
) (string, error) {
	gh := agentDeploy.Spec.Evolution.HumanApproval.GitHub

	token, err := r.readSecretKey(ctx, agentDeploy.Namespace, gh.SecretRef, "GITHUB_TOKEN")
	if err != nil {
		return "", fmt.Errorf("reading GitHub token: %w", err)
	}

	baseBranch := gh.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Branch name: agentroll/evolution/{strategy}/{agent}/{timestamp}
	timestamp := time.Now().UTC().Format("20060102-150405")
	branchName := fmt.Sprintf("agentroll/evolution/%s/%s/%s", strategy, agentDeploy.Name, timestamp)

	// File path for the suggestion.
	filePath := fmt.Sprintf(".agentroll/evolution/%s/%s-%s.md",
		agentDeploy.Name, strategy, timestamp)

	fileContent := fmt.Sprintf("# AgentRoll Evolution Proposal\n\n"+
		"**Strategy**: %s  \n**Agent**: %s  \n**Generated**: %s  \n\n---\n\n%s\n",
		strategy, agentDeploy.Name, time.Now().UTC().Format(time.RFC3339), content)

	apiBase := fmt.Sprintf("https://api.github.com/repos/%s/%s", gh.Owner, gh.Repo)
	prCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// Step 1: Get the SHA of the base branch.
	baseSHA, err := githubGetBranchSHA(prCtx, apiBase, baseBranch, token)
	if err != nil {
		return "", fmt.Errorf("getting base branch SHA: %w", err)
	}

	// Step 2: Create the new branch.
	if err := githubCreateBranch(prCtx, apiBase, branchName, baseSHA, token); err != nil {
		return "", fmt.Errorf("creating branch: %w", err)
	}

	// Step 3: Create the file on the new branch.
	encoded := base64.StdEncoding.EncodeToString([]byte(fileContent))
	if err := githubCreateFile(prCtx, apiBase, filePath, branchName,
		fmt.Sprintf("agentroll: %s evolution proposal for %s", strategy, agentDeploy.Name),
		encoded, token); err != nil {
		return "", fmt.Errorf("creating file: %w", err)
	}

	// Step 4: Open the PR.
	prTitle := fmt.Sprintf("[AgentRoll] %s evolution proposal for %s", strategy, agentDeploy.Name)
	prBody := fmt.Sprintf("## AgentRoll Evolution Proposal\n\n"+
		"**Strategy**: `%s`  \n**Agent**: `%s`  \n**Canary version**: `%s`\n\n"+
		"This PR was automatically generated by AgentRoll's self-evolution loop.\n\n"+
		"### Proposed Change\n\nSee `%s` for the full suggestion.\n\n"+
		"---\n*Generated by AgentRoll at %s*",
		strategy, agentDeploy.Name, agentDeploy.Status.CanaryVersion,
		filePath, time.Now().UTC().Format(time.RFC3339))

	prURL, err := githubCreatePR(prCtx, apiBase, prTitle, prBody, branchName, baseBranch, token)
	if err != nil {
		return "", fmt.Errorf("creating PR: %w", err)
	}

	return prURL, nil
}

// ─── GitHub API helpers ───────────────────────────────────────────────────────

func githubDo(ctx context.Context, method, url string, body interface{}, token string) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API %s %s → %d: %s", method, url, resp.StatusCode, respBody)
	}
	return respBody, nil
}

func githubGetBranchSHA(ctx context.Context, apiBase, branch, token string) (string, error) {
	url := fmt.Sprintf("%s/git/ref/heads/%s", apiBase, branch)
	body, err := githubDo(ctx, http.MethodGet, url, nil, token)
	if err != nil {
		return "", err
	}
	var result struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.Object.SHA, nil
}

func githubCreateBranch(ctx context.Context, apiBase, branch, sha, token string) error {
	url := fmt.Sprintf("%s/git/refs", apiBase)
	_, err := githubDo(ctx, http.MethodPost, url, map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}, token)
	return err
}

func githubCreateFile(ctx context.Context, apiBase, path, branch, message, contentB64, token string) error {
	url := fmt.Sprintf("%s/contents/%s", apiBase, path)
	_, err := githubDo(ctx, http.MethodPut, url, map[string]string{
		"message": message,
		"content": contentB64,
		"branch":  branch,
	}, token)
	return err
}

func githubCreatePR(ctx context.Context, apiBase, title, body, head, base, token string) (string, error) {
	url := fmt.Sprintf("%s/pulls", apiBase)
	respBody, err := githubDo(ctx, http.MethodPost, url, map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}, token)
	if err != nil {
		return "", err
	}
	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	return result.HTMLURL, nil
}
