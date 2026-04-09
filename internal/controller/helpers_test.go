/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"testing"

	rolloutsv1alpha1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ── mapRolloutPhase ─────────────────────────────────────────────────────────

func TestMapRolloutPhase_Healthy(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	r.Status.Phase = rolloutsv1alpha1.RolloutPhaseHealthy
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhaseStable {
		t.Errorf("expected Stable, got %q", got)
	}
}

func TestMapRolloutPhase_Progressing(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	r.Status.Phase = rolloutsv1alpha1.RolloutPhaseProgressing
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhaseProgressing {
		t.Errorf("expected Progressing, got %q", got)
	}
}

func TestMapRolloutPhase_Paused(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	r.Status.Phase = rolloutsv1alpha1.RolloutPhasePaused
	// Paused = still Progressing from user's perspective
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhaseProgressing {
		t.Errorf("expected Progressing for Paused, got %q", got)
	}
}

func TestMapRolloutPhase_Degraded(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	r.Status.Phase = rolloutsv1alpha1.RolloutPhaseDegraded
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhaseDegraded {
		t.Errorf("expected Degraded, got %q", got)
	}
}

func TestMapRolloutPhase_Unknown(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	r.Status.Phase = "SomeUnknownPhase"
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhasePending {
		t.Errorf("expected Pending for unknown phase, got %q", got)
	}
}

func TestMapRolloutPhase_Empty(t *testing.T) {
	r := &rolloutsv1alpha1.Rollout{}
	if got := mapRolloutPhase(r); got != agentrollv1alpha1.PhasePending {
		t.Errorf("expected Pending for empty phase, got %q", got)
	}
}

// ── extractImageTag ─────────────────────────────────────────────────────────

func TestExtractImageTag_WithTag(t *testing.T) {
	if got := extractImageTag("registry.io/my-agent:v1.2.3"); got != "v1.2.3" {
		t.Errorf("expected v1.2.3, got %q", got)
	}
}

func TestExtractImageTag_NoTag(t *testing.T) {
	if got := extractImageTag("registry.io/my-agent"); got != "latest" {
		t.Errorf("expected latest, got %q", got)
	}
}

func TestExtractImageTag_ColonInRegistry(t *testing.T) {
	// Port in registry address — tag after final colon
	if got := extractImageTag("my-registry:5000/agent:v2.0"); got != "v2.0" {
		t.Errorf("expected v2.0, got %q", got)
	}
}

func TestExtractImageTag_LatestTag(t *testing.T) {
	if got := extractImageTag("ghcr.io/user/agent:latest"); got != "latest" {
		t.Errorf("expected latest, got %q", got)
	}
}

// ── buildCompositeVersion ───────────────────────────────────────────────────

func TestBuildCompositeVersion_AllFields(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container: agentrollv1alpha1.AgentContainerSpec{Image: "ghcr.io/x/agent:v3"},
			AgentMeta: agentrollv1alpha1.AgentMetaSpec{
				PromptVersion: "p1",
				ModelVersion:  "claude-opus",
			},
		},
	}
	got := buildCompositeVersion(ad)
	want := "p1.claude-opus.v3"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildCompositeVersion_Defaults(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container: agentrollv1alpha1.AgentContainerSpec{Image: "agent"},
		},
	}
	got := buildCompositeVersion(ad)
	want := "default.default.latest"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ── buildLabels ─────────────────────────────────────────────────────────────

func TestBuildLabels_WithMeta(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Name = "my-agent"
	ad.Spec.AgentMeta.PromptVersion = "pv1"
	ad.Spec.AgentMeta.ModelVersion = "mv1"

	labels := buildLabels(ad, "cv1")
	if labels["agentroll.dev/composite-version"] != "cv1" {
		t.Error("missing composite-version label")
	}
	if labels["agentroll.dev/prompt-version"] != "pv1" {
		t.Error("missing prompt-version label")
	}
	if labels["agentroll.dev/model-version"] != "mv1" {
		t.Error("missing model-version label")
	}
	if labels["app.kubernetes.io/managed-by"] != "agentroll" {
		t.Error("missing managed-by label")
	}
}

func TestBuildLabels_NoMeta(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	ad.Name = "bare-agent"
	labels := buildLabels(ad, "cv")
	if _, ok := labels["agentroll.dev/prompt-version"]; ok {
		t.Error("should not set prompt-version when empty")
	}
	if _, ok := labels["agentroll.dev/model-version"]; ok {
		t.Error("should not set model-version when empty")
	}
}

// ── resourcesOrDefault ──────────────────────────────────────────────────────

func TestResourcesOrDefault_WithValue(t *testing.T) {
	req := corev1.ResourceRequirements{}
	got := resourcesOrDefault(&req)
	// got is always a struct value (non-pointer) — just verify no panic and fields
	_ = got
}

func TestResourcesOrDefault_Nil(t *testing.T) {
	// nil input should return defaults without panic
	got := resourcesOrDefault(nil)
	_ = got // defaults populated — just verify no panic
}

// ── buildPodSpec ────────────────────────────────────────────────────────────

func TestBuildPodSpec_NoOTel(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container: agentrollv1alpha1.AgentContainerSpec{
				Image: "agent:v1",
				Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
			},
		},
	}
	spec := buildPodSpec(ad)
	if len(spec.Containers) != 1 {
		t.Errorf("expected 1 container, got %d", len(spec.Containers))
	}
	if spec.Containers[0].Image != "agent:v1" {
		t.Errorf("wrong image: %q", spec.Containers[0].Image)
	}
}

func TestBuildPodSpec_WithOTel(t *testing.T) {
	enabled := true
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container: agentrollv1alpha1.AgentContainerSpec{Image: "agent:v1"},
			Observability: &agentrollv1alpha1.ObservabilitySpec{
				OpenTelemetry: &agentrollv1alpha1.OTelSpec{
					Enabled: enabled,
				},
			},
		},
	}
	spec := buildPodSpec(ad)
	if len(spec.Containers) != 2 {
		t.Errorf("expected 2 containers (agent + sidecar), got %d", len(spec.Containers))
	}
	// OTEL env var should be injected
	found := false
	for _, env := range spec.Containers[0].Env {
		if env.Name == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			found = true
		}
	}
	if !found {
		t.Error("OTEL_EXPORTER_OTLP_ENDPOINT not injected")
	}
}

func TestBuildPodSpec_OTelCustomEndpoint(t *testing.T) {
	enabled := true
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container: agentrollv1alpha1.AgentContainerSpec{Image: "agent:v1"},
			Observability: &agentrollv1alpha1.ObservabilitySpec{
				OpenTelemetry: &agentrollv1alpha1.OTelSpec{
					Enabled:           enabled,
					CollectorEndpoint: "http://my-collector:4317",
				},
			},
		},
	}
	spec := buildPodSpec(ad)
	if len(spec.Containers) != 2 {
		t.Errorf("expected 2 containers, got %d", len(spec.Containers))
	}
}

func TestBuildPodSpec_ServiceAccount(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Container:          agentrollv1alpha1.AgentContainerSpec{Image: "agent:v1"},
			ServiceAccountName: "custom-sa",
		},
	}
	spec := buildPodSpec(ad)
	if spec.ServiceAccountName != "custom-sa" {
		t.Errorf("expected custom-sa, got %q", spec.ServiceAccountName)
	}
}

// ── validateSemverConstraint ────────────────────────────────────────────────

func TestValidateSemverConstraint_Satisfied(t *testing.T) {
	err := validateSemverConstraint("my-tool", ">=1.0.0", "1.2.3")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateSemverConstraint_NotSatisfied(t *testing.T) {
	err := validateSemverConstraint("my-tool", ">=2.0.0", "1.9.9")
	if err == nil {
		t.Error("expected constraint violation error")
	}
}

func TestValidateSemverConstraint_MalformedConstraint(t *testing.T) {
	// Malformed constraint should not block (lenient)
	err := validateSemverConstraint("my-tool", "not-a-semver", "1.0.0")
	if err != nil {
		t.Errorf("malformed constraint should not return error, got %v", err)
	}
}

func TestValidateSemverConstraint_MalformedVersion(t *testing.T) {
	// Deployed version not parseable — skip check
	err := validateSemverConstraint("my-tool", ">=1.0.0", "not-a-version")
	if err != nil {
		t.Errorf("unparseable deployed version should not return error, got %v", err)
	}
}

// ── isNoCRDError ────────────────────────────────────────────────────────────

func TestIsNoCRDError_Nil(t *testing.T) {
	if isNoCRDError(nil) {
		t.Error("nil error should return false")
	}
}

func TestIsNoCRDError_NoKindRegistered(t *testing.T) {
	err := &fakeError{"no kind is registered for the type"}
	if !isNoCRDError(err) {
		t.Error("expected true for 'no kind is registered'")
	}
}

func TestIsNoCRDError_NoMatches(t *testing.T) {
	err := &fakeError{"no matches for kind \"ScaledObject\" in group"}
	if !isNoCRDError(err) {
		t.Error("expected true for 'no matches for kind'")
	}
}

func TestIsNoCRDError_OtherError(t *testing.T) {
	err := &fakeError{"connection refused"}
	if isNoCRDError(err) {
		t.Error("unexpected true for non-CRD error")
	}
}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

// ── buildKEDATrigger ────────────────────────────────────────────────────────

func TestBuildKEDATrigger_Redis(t *testing.T) {
	qr := &agentrollv1alpha1.QueueReference{Provider: "redis", Address: "redis:6379", QueueName: "tasks"}
	trigger, err := buildKEDATrigger(qr, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trigger["type"] != "redis" {
		t.Errorf("expected type redis, got %v", trigger["type"])
	}
}

func TestBuildKEDATrigger_RabbitMQ(t *testing.T) {
	qr := &agentrollv1alpha1.QueueReference{Provider: "rabbitmq", Address: "amqp://rabbit:5672", QueueName: "tasks"}
	trigger, err := buildKEDATrigger(qr, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trigger["type"] != "rabbitmq" {
		t.Errorf("expected type rabbitmq, got %v", trigger["type"])
	}
}

func TestBuildKEDATrigger_SQS(t *testing.T) {
	qr := &agentrollv1alpha1.QueueReference{Provider: "sqs", Address: "https://sqs.us-east-1.amazonaws.com/123/q", QueueName: "tasks"}
	trigger, err := buildKEDATrigger(qr, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trigger["type"] != "aws-sqs-queue" {
		t.Errorf("expected type aws-sqs-queue, got %v", trigger["type"])
	}
}

func TestBuildKEDATrigger_Unknown(t *testing.T) {
	qr := &agentrollv1alpha1.QueueReference{
		Provider:  "kafka",
		Address:   "kafka:9092",
		QueueName: "tasks",
	}
	_, err := buildKEDATrigger(qr, 5)
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

// ── parseCostThreshold ──────────────────────────────────────────────────────

func TestParseCostThreshold_ValidPercent(t *testing.T) {
	got := parseCostThreshold("200%")
	if got != 2.0 {
		t.Errorf("expected 2.0, got %v", got)
	}
}

func TestParseCostThreshold_NoPercent(t *testing.T) {
	got := parseCostThreshold("150")
	if got != 1.5 {
		t.Errorf("expected 1.5, got %v", got)
	}
}

func TestParseCostThreshold_Invalid(t *testing.T) {
	got := parseCostThreshold("notanumber")
	if got != 2.0 {
		t.Errorf("expected default 2.0, got %v", got)
	}
}

func TestParseCostThreshold_Zero(t *testing.T) {
	got := parseCostThreshold("0%")
	if got != 2.0 {
		t.Errorf("expected default 2.0 for zero, got %v", got)
	}
}

func TestParseCostThreshold_Negative(t *testing.T) {
	got := parseCostThreshold("-10%")
	if got != 2.0 {
		t.Errorf("expected default 2.0 for negative, got %v", got)
	}
}

// ── langfuseSecretName / langfuseHost ───────────────────────────────────────

func TestLangfuseSecretName_Default(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	if got := langfuseSecretName(ad); got != "langfuse-credentials" {
		t.Errorf("expected langfuse-credentials, got %q", got)
	}
}

func TestLangfuseSecretName_Custom(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Observability: &agentrollv1alpha1.ObservabilitySpec{
				Langfuse: &agentrollv1alpha1.LangfuseSpec{SecretRef: "my-secret"},
			},
		},
	}
	if got := langfuseSecretName(ad); got != "my-secret" {
		t.Errorf("expected my-secret, got %q", got)
	}
}

func TestLangfuseHost_Default(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{}
	if got := langfuseHost(ad); got != "https://cloud.langfuse.com" {
		t.Errorf("expected cloud.langfuse.com, got %q", got)
	}
}

func TestLangfuseHost_Custom(t *testing.T) {
	ad := &agentrollv1alpha1.AgentDeployment{
		Spec: agentrollv1alpha1.AgentDeploymentSpec{
			Observability: &agentrollv1alpha1.ObservabilitySpec{
				Langfuse: &agentrollv1alpha1.LangfuseSpec{Endpoint: "https://my-langfuse.io"},
			},
		},
	}
	if got := langfuseHost(ad); got != "https://my-langfuse.io" {
		t.Errorf("expected https://my-langfuse.io, got %q", got)
	}
}

// ── parseDuration ───────────────────────────────────────────────────────────

func TestParseDuration(t *testing.T) {
	got := parseDuration("5m")
	if got == nil {
		t.Error("expected non-nil IntOrString")
	}
	if got.String() != "5m" {
		t.Errorf("expected 5m, got %q", got.String())
	}
}

// ── buildManagedTemplateSpec ────────────────────────────────────────────────

func TestBuildManagedTemplateSpec_QualityCheck(t *testing.T) {
	spec := buildManagedTemplateSpec("agent-quality-check")
	if len(spec.Metrics) == 0 {
		t.Error("expected at least one metric")
	}
	if spec.Metrics[0].Name != "agent-health" {
		t.Errorf("expected agent-health metric, got %q", spec.Metrics[0].Name)
	}
}

func TestBuildManagedTemplateSpec_CostCheck(t *testing.T) {
	spec := buildManagedTemplateSpec("agent-cost-check")
	if len(spec.Metrics) == 0 {
		t.Error("expected at least one metric")
	}
}

// ── setStatusConditions ─────────────────────────────────────────────────────

func TestSetStatusConditions_AllPhases(t *testing.T) {
	phases := []agentrollv1alpha1.AgentDeploymentPhase{
		agentrollv1alpha1.PhaseStable,
		agentrollv1alpha1.PhaseProgressing,
		agentrollv1alpha1.PhaseDegraded,
		agentrollv1alpha1.PhaseRollingBack,
		agentrollv1alpha1.PhasePending,
	}

	r := &AgentDeploymentReconciler{Recorder: record.NewFakeRecorder(10)}
	for _, phase := range phases {
		ad := &agentrollv1alpha1.AgentDeployment{}
		ad.Status.Phase = phase
		ad.Status.StableVersion = "v1"
		r.setStatusConditions(ad)

		if len(ad.Status.Conditions) != 3 {
			t.Errorf("phase %q: expected 3 conditions, got %d", phase, len(ad.Status.Conditions))
		}
	}
}

// ── stableRSCompositeVersion ────────────────────────────────────────────────
// No ReplicaSets in envtest by default for this test → returns "".

func TestStableRSCompositeVersion_Empty(t *testing.T) {
	// Exercise the function with an empty client — it should return "" gracefully.
	// This requires k8sClient but runs in the same test binary as the Ginkgo suite.
	// Since we're in a Go testing.T test (not Ginkgo), k8sClient is nil here.
	// Use a nil guard to skip when outside the envtest environment.
	if k8sClient == nil {
		t.Skip("k8sClient not initialized outside of Ginkgo envtest suite")
	}
	r := &AgentDeploymentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	got := r.stableRSCompositeVersion(context.Background(), "default", "test-rollout", "abc123")
	if got != "" {
		t.Errorf("expected empty string when no ReplicaSets exist, got %q", got)
	}
}

// ── emitPhaseEvent ──────────────────────────────────────────────────────────

func TestEmitPhaseEvent_AllPhases(t *testing.T) {
	phases := []agentrollv1alpha1.AgentDeploymentPhase{
		agentrollv1alpha1.PhaseStable,
		agentrollv1alpha1.PhaseProgressing,
		agentrollv1alpha1.PhaseDegraded,
		agentrollv1alpha1.PhaseRollingBack,
		agentrollv1alpha1.PhasePending, // default/no-op case
	}

	for _, phase := range phases {
		rec := record.NewFakeRecorder(10)
		r := &AgentDeploymentReconciler{Recorder: rec}
		ad := &agentrollv1alpha1.AgentDeployment{}
		ad.Status.Phase = phase
		ad.Status.StableVersion = "v1"
		// Should not panic for any phase
		r.emitPhaseEvent(ad)
	}
}
