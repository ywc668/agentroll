/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentDeploymentSpec defines the desired state of an AI agent deployment
type AgentDeploymentSpec struct {
	// Container defines the agent's container configuration.
	// Framework-agnostic: works with LangGraph, CrewAI, OpenAI Agents SDK, or any custom agent.
	Container AgentContainerSpec `json:"container"`

	// AgentMeta captures the composite version identity of the agent.
	// This is what makes agent deployments different from microservices:
	// behavior depends on prompt + model + tools + memory, not just code.
	// +optional
	AgentMeta AgentMetaSpec `json:"agentMeta,omitempty"`

	// Rollout defines the progressive delivery strategy.
	// Built on top of Argo Rollouts — not reinventing the wheel.
	Rollout RolloutSpec `json:"rollout"`

	// Rollback defines automatic rollback conditions.
	// +optional
	Rollback *RollbackSpec `json:"rollback,omitempty"`

	// Observability configures agent-specific monitoring and tracing.
	// +optional
	Observability *ObservabilitySpec `json:"observability,omitempty"`

	// Scaling defines autoscaling behavior.
	// Defaults to queue-depth based scaling (not CPU — agents are I/O bound).
	// +optional
	Scaling *ScalingSpec `json:"scaling,omitempty"`

	// ServiceAccountName is the Kubernetes ServiceAccount for the agent pods.
	// Agents often need specific RBAC permissions (e.g., read cluster resources).
	// If not set, the default ServiceAccount of the namespace is used.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Replicas is the desired number of agent pod replicas.
	// Ignored if Scaling is configured.
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// DependsOn lists names of other AgentDeployments (in the same namespace) that
	// must be in Stable phase before this agent's canary is allowed to progress.
	// Use this for A2A (agent-to-agent) coordination: if agent B calls agent A,
	// declare A in B's DependsOn so a degraded A blocks B's canary promotion.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Evolution configures the self-evolution loop for this agent.
	// When enabled, the controller analyses canary outcomes and proposes or applies
	// improvements: threshold tuning, prompt optimisation, or model version bumps.
	// Disabled by default — opt in explicitly.
	// +optional
	Evolution *EvolutionSpec `json:"evolution,omitempty"`

	// Evaluation configures the LLM-as-judge quality scoring layer.
	// When set, the controller creates an agent-judge-check AnalysisTemplate
	// that scores canary responses with an LLM and writes quality scores to Langfuse.
	// +optional
	Evaluation *EvaluationSpec `json:"evaluation,omitempty"`
}

// AgentContainerSpec defines the container configuration for the agent.
type AgentContainerSpec struct {
	// Image is the container image for the agent.
	Image string `json:"image"`

	// Env defines environment variables for the agent container.
	// Typically includes LLM_PROVIDER, LLM_MODEL, API keys (via secretKeyRef), etc.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources defines compute resource requirements.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Ports defines the ports exposed by the agent container.
	// +optional
	Ports []corev1.ContainerPort `json:"ports,omitempty"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args provides arguments to the entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`
}

// AgentMetaSpec captures the composite version identity of an agent.
// An agent's behavior is determined by 4 interdependent layers.
// Changing any one can alter behavior unpredictably.
type AgentMetaSpec struct {
	// PromptVersion is a Git commit ref, tag, or semantic version
	// identifying the prompt/system context version.
	// This is the most frequently changed layer.
	// +optional
	PromptVersion string `json:"promptVersion,omitempty"`

	// ModelVersion identifies the LLM model being used (e.g., "claude-sonnet-4-20250514").
	// Model changes can dramatically alter agent behavior even with identical prompts.
	// +optional
	ModelVersion string `json:"modelVersion,omitempty"`

	// ModelProvider identifies the LLM provider (e.g., "anthropic", "openai", "local").
	// +optional
	ModelProvider string `json:"modelProvider,omitempty"`

	// ToolDependencies declares MCP tool servers or other tool services
	// that this agent depends on, with version constraints.
	// +optional
	ToolDependencies []ToolDependency `json:"toolDependencies,omitempty"`

	// Labels are arbitrary key-value pairs for organizing and filtering agents.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ToolDependency declares a dependency on an external tool service.
type ToolDependency struct {
	// Name is the identifier of the tool service (e.g., "crm-mcp-server").
	Name string `json:"name"`

	// Version is a semver constraint (e.g., ">=1.2.0", "~1.3").
	// +optional
	Version string `json:"version,omitempty"`

	// Endpoint is the service endpoint if not using Kubernetes service discovery.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// RolloutSpec defines how the agent should be progressively delivered.
type RolloutSpec struct {
	// Strategy is the rollout strategy: "canary" or "blueGreen".
	// +kubebuilder:validation:Enum=canary;blueGreen
	// +kubebuilder:default=canary
	Strategy string `json:"strategy"`

	// Steps defines the canary progression steps.
	// Each step can specify a traffic weight and an optional analysis check.
	// +optional
	Steps []RolloutStep `json:"steps,omitempty"`
}

// RolloutStep defines a single step in the canary progression.
type RolloutStep struct {
	// SetWeight sets the percentage of traffic routed to the canary.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	SetWeight int32 `json:"setWeight"`

	// Pause defines how long to wait at this step before proceeding.
	// +optional
	Pause *PauseSpec `json:"pause,omitempty"`

	// Analysis references an AnalysisTemplate to run at this step.
	// If the analysis fails, the rollout is aborted and rolled back.
	// +optional
	Analysis *StepAnalysis `json:"analysis,omitempty"`
}

// PauseSpec defines a pause duration.
type PauseSpec struct {
	// Duration is how long to pause (e.g., "5m", "1h").
	// +optional
	Duration string `json:"duration,omitempty"`
}

// StepAnalysis references an AnalysisTemplate for evaluation-gated progression.
type StepAnalysis struct {
	// TemplateRef is the name of the AnalysisTemplate to use.
	// AgentRoll ships with pre-built templates: agent-quality-check,
	// agent-cost-check, agent-tool-success-rate, etc.
	TemplateRef string `json:"templateRef"`
}

// RollbackSpec defines conditions under which automatic rollback should occur.
type RollbackSpec struct {
	// OnFailedAnalysis triggers rollback when any analysis step fails.
	// +kubebuilder:default=true
	OnFailedAnalysis bool `json:"onFailedAnalysis"`

	// OnCostSpike triggers rollback when token cost exceeds the threshold
	// relative to the stable version's baseline.
	// +optional
	OnCostSpike *CostSpikeSpec `json:"onCostSpike,omitempty"`
}

// CostSpikeSpec defines a cost-based rollback trigger.
type CostSpikeSpec struct {
	// Threshold is the percentage above baseline that triggers rollback.
	// e.g., "200%" means rollback if cost exceeds 2x the stable version.
	Threshold string `json:"threshold"`
}

// ObservabilitySpec configures agent-specific monitoring and tracing.
type ObservabilitySpec struct {
	// Langfuse configures integration with Langfuse for agent trace data.
	// Langfuse traces are used as data sources for canary analysis.
	// +optional
	Langfuse *LangfuseSpec `json:"langfuse,omitempty"`

	// OpenTelemetry enables auto-injection of an OTel sidecar.
	// +optional
	OpenTelemetry *OTelSpec `json:"opentelemetry,omitempty"`
}

// LangfuseSpec configures Langfuse integration.
type LangfuseSpec struct {
	// Endpoint is the Langfuse server URL.
	Endpoint string `json:"endpoint"`

	// ProjectID identifies the Langfuse project for this agent.
	// +optional
	ProjectID string `json:"projectId,omitempty"`

	// SecretRef references a Kubernetes Secret containing the Langfuse API keys.
	// Expected keys: LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// OTelSpec configures OpenTelemetry integration.
type OTelSpec struct {
	// Enabled controls whether an OTel collector sidecar is injected.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// CollectorEndpoint overrides the default OTel collector endpoint.
	// +optional
	CollectorEndpoint string `json:"collectorEndpoint,omitempty"`
}

// ScalingSpec defines autoscaling behavior for agent workloads.
type ScalingSpec struct {
	// MinReplicas is the minimum number of agent pod replicas.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	MinReplicas int32 `json:"minReplicas"`

	// MaxReplicas is the maximum number of agent pod replicas.
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`

	// Metric is the scaling metric: "queue-depth" (recommended for agents),
	// "cpu", "memory", or "custom".
	// Agents are typically I/O bound (waiting for LLM API responses),
	// so CPU-based scaling is ineffective. Use queue-depth instead.
	// +kubebuilder:validation:Enum=queue-depth;cpu;memory;custom
	// +kubebuilder:default=queue-depth
	Metric string `json:"metric"`

	// TargetValue is the per-pod target for the scaling metric.
	// For queue-depth: number of pending tasks per pod.
	// For cpu/memory: percentage utilization.
	// +kubebuilder:default=5
	TargetValue int32 `json:"targetValue"`

	// QueueRef identifies the queue to monitor for queue-depth scaling.
	// +optional
	QueueRef *QueueReference `json:"queueRef,omitempty"`
}

// QueueReference identifies a task queue for scaling metrics.
type QueueReference struct {
	// Provider is the queue backend: "redis", "rabbitmq", "sqs".
	Provider string `json:"provider"`

	// Address is the queue service address.
	Address string `json:"address"`

	// QueueName is the specific queue/topic to monitor.
	QueueName string `json:"queueName"`
}

// AgentDeploymentStatus defines the observed state of an AgentDeployment.
type AgentDeploymentStatus struct {
	// Phase represents the current lifecycle phase.
	// +optional
	Phase AgentDeploymentPhase `json:"phase,omitempty"`

	// StableVersion is the currently stable (fully rolled out) composite version.
	// +optional
	StableVersion string `json:"stableVersion,omitempty"`

	// CanaryVersion is the version currently being tested via canary.
	// +optional
	CanaryVersion string `json:"canaryVersion,omitempty"`

	// CanaryWeight is the current percentage of traffic going to the canary.
	// +optional
	CanaryWeight int32 `json:"canaryWeight,omitempty"`

	// LastAnalysisResult summarizes the most recent analysis outcome.
	// +optional
	LastAnalysisResult *AnalysisResult `json:"lastAnalysisResult,omitempty"`

	// Conditions represent the latest observations of the deployment's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Evolution reflects the current state of the self-evolution loop.
	// Populated only after the evolution controller has completed at least one evaluation.
	// +optional
	Evolution *EvolutionStatus `json:"evolution,omitempty"`

	// EvalHistory records the last 50 LLM-as-judge quality scores in chronological order.
	// Oldest entries are dropped when the buffer is full.
	// Used as the primary quality signal by the threshold tuner and plateau detector.
	// +optional
	EvalHistory []EvalHistoryEntry `json:"evalHistory,omitempty"`

	// PromptLineage records the last 20 prompt A/B experiment outcomes in
	// chronological order. Answers "did this prompt change help?"
	// +optional
	PromptLineage []PromptLineageEntry `json:"promptLineage,omitempty"`
}

// AgentDeploymentPhase represents the lifecycle phase of an agent deployment.
// +kubebuilder:validation:Enum=Pending;Progressing;Stable;Degraded;RollingBack
type AgentDeploymentPhase string

const (
	PhasePending     AgentDeploymentPhase = "Pending"
	PhaseProgressing AgentDeploymentPhase = "Progressing"
	PhaseStable      AgentDeploymentPhase = "Stable"
	PhaseDegraded    AgentDeploymentPhase = "Degraded"
	PhaseRollingBack AgentDeploymentPhase = "RollingBack"
)

// AnalysisResult captures the outcome of an evaluation gate.
type AnalysisResult struct {
	// Status is the analysis result: "Successful", "Failed", "Inconclusive".
	Status string `json:"status"`

	// Message provides human-readable details about the result.
	// +optional
	Message string `json:"message,omitempty"`

	// Metrics contains the measured metric values.
	// +optional
	Metrics map[string]string `json:"metrics,omitempty"`

	// CompletedAt is when the analysis finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Stable",type=string,JSONPath=`.status.stableVersion`
// +kubebuilder:printcolumn:name="Canary",type=string,JSONPath=`.status.canaryVersion`
// +kubebuilder:printcolumn:name="Weight",type=integer,JSONPath=`.status.canaryWeight`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentDeployment is the Schema for the agentdeployments API.
// It represents a deployable AI agent with evaluation-gated progressive delivery.
type AgentDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentDeploymentSpec   `json:"spec,omitempty"`
	Status AgentDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentDeploymentList contains a list of AgentDeployment resources.
type AgentDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentDeployment `json:"items"`
}

// ─── Evolution ───────────────────────────────────────────────────────────────

// EvolutionSpec configures the self-evolution loop for an AgentDeployment.
// The loop fires after each canary (on fail, on a schedule, or both) and runs
// one or more strategies that analyse outcomes and propose or apply improvements.
type EvolutionSpec struct {
	// Enabled controls whether the self-evolution loop is active.
	// Set to true to opt in; all other fields are ignored when false.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Strategy selects which evolution strategies to run.
	//
	// threshold-tuner  — adjusts quality-gate thresholds from historical AnalysisRun
	//                    outcomes without calling an LLM.
	// prompt-optimizer — reads failing Langfuse traces, calls an LLM, and opens a
	//                    GitHub PR with suggested prompt rewrites.
	// model-upgrader   — proposes a model version bump when quality plateaus across
	//                    N consecutive canaries.
	// all              — runs all three strategies in order.
	//
	// +kubebuilder:validation:Enum=threshold-tuner;prompt-optimizer;model-upgrader;all
	// +kubebuilder:default=all
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// Trigger determines when the evolution loop fires.
	//
	// on-canary-fail — fires immediately after a canary is aborted by a failed AnalysisRun.
	// periodic       — fires on a fixed schedule regardless of rollout outcome.
	// both           — fires on canary failure AND on the periodic schedule.
	//
	// +kubebuilder:validation:Enum=on-canary-fail;periodic;both
	// +kubebuilder:default=on-canary-fail
	// +optional
	Trigger string `json:"trigger,omitempty"`

	// Schedule is a cron expression that controls how often the evolution loop fires
	// when trigger is "periodic" or "both" (e.g., "0 2 * * *" for daily at 02:00 UTC).
	// Required when trigger is "periodic" or "both"; ignored for "on-canary-fail".
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// ConsecutiveCanariesForPlateau is the number of consecutive successful canaries
	// with no quality improvement that triggers the model-upgrader strategy.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:default=3
	// +optional
	ConsecutiveCanariesForPlateau *int32 `json:"consecutiveCanariesForPlateau,omitempty"`

	// Optimizer holds LLM connection details used by the prompt-optimizer and
	// model-upgrader strategies. Required when strategy is "prompt-optimizer",
	// "model-upgrader", or "all".
	// +optional
	Optimizer *EvolutionOptimizerSpec `json:"optimizer,omitempty"`

	// HumanApproval configures human-in-the-loop review via GitHub PRs.
	// When set, proposed improvements are opened as pull requests rather than
	// applied directly.
	// +optional
	HumanApproval *HumanApprovalSpec `json:"humanApproval,omitempty"`

	// PromptConfigMap is the name of a ConfigMap (in the same namespace) that
	// holds versioned system prompts. Expected key: "system_prompt".
	//
	// When set, the prompt-optimizer strategy writes its improved prompt directly
	// to this ConfigMap (key: "system_prompt") instead of — or in addition to —
	// opening a GitHub PR. The controller then injects a SYSTEM_PROMPT env var
	// into the agent container sourced from this ConfigMap, so the next pod
	// restart picks up the new prompt without a new image build.
	//
	// When humanApproval is also configured, the PR is opened AND the ConfigMap
	// is NOT updated automatically (human review required first).
	// +optional
	PromptConfigMap string `json:"promptConfigMap,omitempty"`

	// PromptExperiment is the name of a PromptVariant (in the same namespace) to
	// A/B test against the current stable prompt.
	// When set, the controller creates a per-experiment ConfigMap with the variant's
	// system prompt and injects it into canary pods as SYSTEM_PROMPT.
	// Stable pods retain the existing SYSTEM_PROMPT from their creation time.
	// After PromptExperimentMinSamples judge scores are collected, the controller
	// runs a Welch's t-test and auto-promotes or rejects the variant.
	// +optional
	PromptExperiment string `json:"promptExperiment,omitempty"`

	// PromptExperimentMinSamples is the number of LLM-as-judge quality scores to
	// collect before running the t-test and making an auto-promotion decision.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:default=5
	// +optional
	PromptExperimentMinSamples *int32 `json:"promptExperimentMinSamples,omitempty"`
}

// EvolutionOptimizerSpec configures the LLM used by the prompt-optimizer and
// model-upgrader strategies.
type EvolutionOptimizerSpec struct {
	// Model is the LLM model identifier (e.g., "claude-sonnet-4-20250514", "gpt-4o").
	Model string `json:"model"`

	// Provider identifies the LLM API provider.
	// +kubebuilder:validation:Enum=anthropic;openai
	// +kubebuilder:default=anthropic
	// +optional
	Provider string `json:"provider,omitempty"`

	// SecretRef is the name of a Kubernetes Secret in the same namespace containing
	// the provider API key. Expected key: API_KEY.
	SecretRef string `json:"secretRef"`
}

// HumanApprovalSpec configures human-in-the-loop review for proposed improvements.
type HumanApprovalSpec struct {
	// GitHub configures pull request creation for human review.
	// +optional
	GitHub *GitHubSpec `json:"github,omitempty"`
}

// GitHubSpec configures pull request creation in a GitHub repository.
type GitHubSpec struct {
	// Owner is the GitHub user or organisation that owns the repository.
	Owner string `json:"owner"`

	// Repo is the repository name (without the owner prefix).
	Repo string `json:"repo"`

	// BaseBranch is the target branch for pull requests.
	// +kubebuilder:default=main
	// +optional
	BaseBranch string `json:"baseBranch,omitempty"`

	// SecretRef is the name of a Kubernetes Secret containing the GitHub token.
	// Expected key: GITHUB_TOKEN. Token must have contents:write and pull_requests:write.
	SecretRef string `json:"secretRef"`
}

// EvolutionStatus reflects the observed state of the self-evolution loop.
type EvolutionStatus struct {
	// LastProposal is a human-readable summary of the most recent evolution action.
	// +optional
	LastProposal string `json:"lastProposal,omitempty"`

	// ProposalCount is the total number of evolution proposals generated.
	// +optional
	ProposalCount int32 `json:"proposalCount,omitempty"`

	// LastProposalAt is the time the most recent proposal was generated.
	// +optional
	LastProposalAt *metav1.Time `json:"lastProposalAt,omitempty"`

	// NextEvalAt is the scheduled time for the next evolution evaluation.
	// Populated only when trigger is "periodic" or "both".
	// +optional
	NextEvalAt *metav1.Time `json:"nextEvalAt,omitempty"`

	// TunedThresholds records the current adjusted threshold values produced by the
	// threshold-tuner strategy. Keys are metric names (e.g., "max_latency_ms"); values
	// are the adjusted thresholds as decimal strings (e.g., "8523.4200").
	// These are injected as env vars into the analysis Job containers on the next
	// AnalysisTemplate reconcile, replacing the hardcoded defaults.
	// +optional
	TunedThresholds map[string]string `json:"tunedThresholds,omitempty"`

	// History records the last 20 evolution loop executions in chronological order.
	// Oldest entries are dropped when the buffer is full.
	// +optional
	History []EvolutionHistoryEntry `json:"history,omitempty"`
}

// EvolutionHistoryEntry records a single execution of the evolution loop.
type EvolutionHistoryEntry struct {
	// At is the timestamp when this evolution loop execution fired.
	At metav1.Time `json:"at"`

	// Strategy is the name of the strategy that produced this entry
	// (e.g., "threshold-tuner", "prompt-optimizer", "model-upgrader").
	Strategy string `json:"strategy"`

	// Description is a brief human-readable summary of what the strategy did
	// (e.g., "adjusted max_latency_ms→8523.4200, min_success_rate→0.8750").
	Description string `json:"description"`

	// Phase is the AgentDeployment phase that triggered the evolution loop
	// (e.g., "Degraded", "Stable"). Empty for periodic triggers.
	// +optional
	Phase string `json:"phase,omitempty"`
}

// ─── Evaluation ──────────────────────────────────────────────────────────────

// EvaluationSpec configures the LLM-as-judge quality scoring layer.
// When set, the controller creates an agent-judge-check AnalysisTemplate that
// sends test queries to the canary, scores responses with an LLM judge, and
// writes numeric quality scores (0.0–1.0) to Langfuse as judge_quality_score.
type EvaluationSpec struct {
	// SecretRef is the name of a Kubernetes Secret (in the same namespace)
	// containing the judge LLM API key. Expected key: API_KEY.
	SecretRef string `json:"secretRef"`

	// JudgeModel is the LLM model ID used for evaluation.
	// Defaults to "claude-haiku-4-5-20251001" when empty.
	// +optional
	JudgeModel string `json:"judgeModel,omitempty"`

	// JudgeProvider is the LLM API provider.
	// +kubebuilder:validation:Enum=anthropic;openai
	// +kubebuilder:default=anthropic
	// +optional
	JudgeProvider string `json:"judgeProvider,omitempty"`

	// MinScore is the minimum acceptable mean quality score (0.0–1.0).
	// The analysis Job exits 0 (pass) if the mean score >= MinScore, 1 (fail) otherwise.
	// +kubebuilder:default="0.7"
	// +optional
	MinScore string `json:"minScore,omitempty"`

	// ConfigMap is the name of a ConfigMap (in the same namespace) holding
	// evaluation configuration. Supported keys:
	//   "rubric"       — evaluation criteria text injected as EVAL_RUBRIC
	//   "test_prompts" — JSON array of test input strings injected as TEST_QUERIES
	// When absent, the judge runner uses its built-in default rubric and queries.
	// +optional
	ConfigMap string `json:"configMap,omitempty"`
}

// EvalHistoryEntry records the quality score from a single LLM-as-judge evaluation run.
type EvalHistoryEntry struct {
	// At is when the evaluation ran.
	At metav1.Time `json:"at"`

	// CompositeVersion is the agent composite version that was evaluated.
	CompositeVersion string `json:"compositeVersion"`

	// QualityScore is the mean judge score for this run (0.0–1.0).
	QualityScore float64 `json:"qualityScore"`

	// Verdict is "pass" if QualityScore >= MinScore, "fail" otherwise.
	Verdict string `json:"verdict"`
}

// ─── Prompt Lineage ──────────────────────────────────────────────────────────

// PromptLineageEntry records the outcome of a single prompt A/B experiment.
type PromptLineageEntry struct {
	// VariantName is the name of the PromptVariant that was tested.
	VariantName string `json:"variantName"`

	// ParentVersion is the prompt version the variant was derived from.
	// +optional
	ParentVersion string `json:"parentVersion,omitempty"`

	// Hypothesis is why the variant was expected to improve quality.
	// +optional
	Hypothesis string `json:"hypothesis,omitempty"`

	// VariantMean is the mean LLM-as-judge score for the variant (0.0–1.0).
	VariantMean float64 `json:"variantMean"`

	// ControlMean is the mean LLM-as-judge score for the control (0.0–1.0).
	ControlMean float64 `json:"controlMean"`

	// PValue is the Welch's t-test p-value (< 0.05 = statistically significant).
	PValue float64 `json:"pValue"`

	// Outcome is "promoted" if the variant won, "rejected" otherwise.
	Outcome string `json:"outcome"`

	// At is when the experiment concluded.
	At metav1.Time `json:"at"`
}

func init() {
	SchemeBuilder.Register(&AgentDeployment{}, &AgentDeploymentList{})
}
