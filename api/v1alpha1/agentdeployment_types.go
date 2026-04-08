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

func init() {
	SchemeBuilder.Register(&AgentDeployment{}, &AgentDeploymentList{})
}
