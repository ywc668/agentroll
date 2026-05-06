/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ─── ToolExperiment ──────────────────────────────────────────────────────────

// ToolExperimentPhase represents the lifecycle stage of a tool A/B experiment.
// +kubebuilder:validation:Enum=Pending;Testing;Promoted;Rejected
type ToolExperimentPhase string

const (
	ToolExperimentPhasePending  ToolExperimentPhase = "Pending"
	ToolExperimentPhaseTesting  ToolExperimentPhase = "Testing"
	ToolExperimentPhasePromoted ToolExperimentPhase = "Promoted"
	ToolExperimentPhaseRejected ToolExperimentPhase = "Rejected"
)

// ToolExperimentSpec defines the desired state of a tool A/B experiment.
type ToolExperimentSpec struct {
	// AgentDeploymentRef is the name of the AgentDeployment (in the same namespace)
	// that this experiment is being tested against.
	AgentDeploymentRef string `json:"agentDeploymentRef"`

	// AdditionalTools lists MCP tool services to inject into canary pods during
	// the experiment. These are added on top of the agent's declared toolDependencies.
	// +optional
	AdditionalTools []ToolDependency `json:"additionalTools,omitempty"`

	// RemovedTools lists the names of tools from spec.agentMeta.toolDependencies
	// to exclude from canary pods during the experiment.
	// +optional
	RemovedTools []string `json:"removedTools,omitempty"`

	// ParentToolsHash is the toolsHash of the stable configuration this experiment
	// was derived from. Recorded in the AgentDeployment's tool lineage for traceability.
	// +optional
	ParentToolsHash string `json:"parentToolsHash,omitempty"`

	// Hypothesis describes why this tool configuration is expected to improve quality.
	// Recorded in the tool lineage for future reference.
	// +optional
	Hypothesis string `json:"hypothesis,omitempty"`
}

// ToolExperimentStatus reflects the observed state of the tool experiment.
type ToolExperimentStatus struct {
	// Phase is the current experiment lifecycle stage.
	// +optional
	Phase ToolExperimentPhase `json:"phase,omitempty"`

	// ExperimentStartedAt is when the experiment transitioned from Pending to Testing.
	// Used to partition eval history scores into control (before) and variant (after).
	// +optional
	ExperimentStartedAt *metav1.Time `json:"experimentStartedAt,omitempty"`

	// VariantScores is the list of LLM-as-judge quality scores collected while Testing.
	// +optional
	VariantScores []float64 `json:"variantScores,omitempty"`

	// VariantMeanScore is the mean of VariantScores. Populated after the t-test runs.
	// +optional
	VariantMeanScore float64 `json:"variantMeanScore,omitempty"`

	// ControlMeanScore is the mean of the control (stable) scores used for comparison.
	// +optional
	ControlMeanScore float64 `json:"controlMeanScore,omitempty"`

	// PValue is the Welch's t-test p-value comparing variant vs control scores.
	// Values < 0.05 indicate statistically significant difference.
	// +optional
	PValue float64 `json:"pValue,omitempty"`

	// Message is a human-readable summary of the experiment outcome.
	// +optional
	Message string `json:"message,omitempty"`

	// PromotedAt is when the experiment was promoted to stable.
	// +optional
	PromotedAt *metav1.Time `json:"promotedAt,omitempty"`

	// RejectedAt is when the experiment was rejected.
	// +optional
	RejectedAt *metav1.Time `json:"rejectedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agentDeploymentRef`
// +kubebuilder:printcolumn:name="P-Value",type=number,JSONPath=`.status.pValue`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ToolExperiment is the Schema for the toolexperiments API.
// It describes a candidate MCP tool configuration for A/B testing against the current
// stable tool set. The AgentDeploymentReconciler drives the experiment lifecycle when
// the parent AgentDeployment's spec.evolution.toolExperiment references this resource.
type ToolExperiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ToolExperimentSpec   `json:"spec,omitempty"`
	Status ToolExperimentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ToolExperimentList contains a list of ToolExperiment resources.
type ToolExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ToolExperiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ToolExperiment{}, &ToolExperimentList{})
}
