/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ─── PromptVariant ───────────────────────────────────────────────────────────

// PromptVariantPhase represents the lifecycle stage of a prompt A/B experiment.
// +kubebuilder:validation:Enum=Pending;Testing;Promoted;Rejected
type PromptVariantPhase string

const (
	PromptVariantPhasePending  PromptVariantPhase = "Pending"
	PromptVariantPhaseTesting  PromptVariantPhase = "Testing"
	PromptVariantPhasePromoted PromptVariantPhase = "Promoted"
	PromptVariantPhaseRejected PromptVariantPhase = "Rejected"
)

// PromptVariantSpec defines the desired state of a prompt A/B experiment.
type PromptVariantSpec struct {
	// AgentDeploymentRef is the name of the AgentDeployment (in the same namespace)
	// that this variant is being tested against.
	AgentDeploymentRef string `json:"agentDeploymentRef"`

	// SystemPrompt is the candidate system prompt text to evaluate.
	SystemPrompt string `json:"systemPrompt"`

	// ParentVersion is the prompt version this variant was derived from.
	// Recorded in the AgentDeployment's prompt lineage for traceability.
	// +optional
	ParentVersion string `json:"parentVersion,omitempty"`

	// Hypothesis describes why this variant is expected to perform better.
	// Recorded in the prompt lineage for future reference.
	// +optional
	Hypothesis string `json:"hypothesis,omitempty"`
}

// PromptVariantStatus reflects the observed state of the prompt experiment.
type PromptVariantStatus struct {
	// Phase is the current experiment lifecycle stage.
	// +optional
	Phase PromptVariantPhase `json:"phase,omitempty"`

	// ExperimentStartedAt is when the experiment transitioned from Pending to Testing.
	// Used to partition eval history scores into control (before) and variant (after).
	// +optional
	ExperimentStartedAt *metav1.Time `json:"experimentStartedAt,omitempty"`

	// VariantScores is the list of LLM-as-judge quality scores collected while Testing.
	// Scores are 0.0–1.0; collected from status.evalHistory of the AgentDeployment.
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

	// PromotedAt is when the variant was promoted to stable.
	// +optional
	PromotedAt *metav1.Time `json:"promotedAt,omitempty"`

	// RejectedAt is when the variant was rejected.
	// +optional
	RejectedAt *metav1.Time `json:"rejectedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agentDeploymentRef`
// +kubebuilder:printcolumn:name="P-Value",type=number,JSONPath=`.status.pValue`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromptVariant is the Schema for the promptvariants API.
// It holds a candidate system prompt for A/B testing against the current stable prompt.
// The AgentDeploymentReconciler drives the experiment lifecycle automatically when
// the parent AgentDeployment's spec.evolution.promptExperiment references this resource.
type PromptVariant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromptVariantSpec   `json:"spec,omitempty"`
	Status PromptVariantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromptVariantList contains a list of PromptVariant resources.
type PromptVariantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromptVariant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromptVariant{}, &PromptVariantList{})
}
