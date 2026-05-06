/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package controller

import (
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentrollv1alpha1 "github.com/ywc668/agentroll/api/v1alpha1"
)

// ── toolsHashFromDeps ─────────────────────────────────────────────────────────

func TestToolsHashFromDeps_StableOrderIndependent(t *testing.T) {
	deps1 := []agentrollv1alpha1.ToolDependency{
		{Name: "kubectl-get", Version: "v1.0.0"},
		{Name: "kubectl-describe", Version: "v1.0.0"},
	}
	deps2 := []agentrollv1alpha1.ToolDependency{
		{Name: "kubectl-describe", Version: "v1.0.0"},
		{Name: "kubectl-get", Version: "v1.0.0"},
	}
	h1 := toolsHashFromDeps(deps1)
	h2 := toolsHashFromDeps(deps2)
	if h1 != h2 {
		t.Errorf("expected order-independent hash, got %q vs %q", h1, h2)
	}
}

func TestToolsHashFromDeps_ChangesOnVersionChange(t *testing.T) {
	deps1 := []agentrollv1alpha1.ToolDependency{{Name: "kubectl-get", Version: "v1.0.0"}}
	deps2 := []agentrollv1alpha1.ToolDependency{{Name: "kubectl-get", Version: "v1.1.0"}}
	h1 := toolsHashFromDeps(deps1)
	h2 := toolsHashFromDeps(deps2)
	if h1 == h2 {
		t.Error("expected different hashes for different tool versions")
	}
}

func TestToolsHashFromDeps_Length(t *testing.T) {
	deps := []agentrollv1alpha1.ToolDependency{{Name: "kubectl-get", Version: "v1.0.0"}}
	h := toolsHashFromDeps(deps)
	if len(h) != 8 {
		t.Errorf("expected 8-char hash, got %d chars: %q", len(h), h)
	}
}

// ── buildCompositeVersion (tools extension) ───────────────────────────────────

func TestBuildCompositeVersion_ThreePartsWithoutTools(t *testing.T) {
	agentDeploy := &agentrollv1alpha1.AgentDeployment{}
	agentDeploy.Spec.AgentMeta.PromptVersion = "v1"
	agentDeploy.Spec.AgentMeta.ModelVersion = "claude-sonnet"
	agentDeploy.Spec.Container.Image = "myagent:abc123"

	cv := buildCompositeVersion(agentDeploy)
	if strings.Count(cv, ".") != 2 {
		t.Errorf("expected 3-part composite version (2 dots), got %q", cv)
	}
}

func TestBuildCompositeVersion_FourPartsWithTools(t *testing.T) {
	agentDeploy := &agentrollv1alpha1.AgentDeployment{}
	agentDeploy.Spec.AgentMeta.PromptVersion = "v1"
	agentDeploy.Spec.AgentMeta.ModelVersion = "claude-sonnet"
	agentDeploy.Spec.Container.Image = "myagent:abc123"
	agentDeploy.Spec.AgentMeta.ToolDependencies = []agentrollv1alpha1.ToolDependency{
		{Name: "kubectl-get", Version: "v1.0.0"},
	}

	cv := buildCompositeVersion(agentDeploy)
	if strings.Count(cv, ".") != 3 {
		t.Errorf("expected 4-part composite version (3 dots), got %q", cv)
	}
}

// ── appendToolLineage ─────────────────────────────────────────────────────────

func TestAppendToolLineage_Basic(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	entry := agentrollv1alpha1.ToolLineageEntry{
		ExperimentName: "exp-add-kubectl-exec",
		VariantMean:    0.85,
		ControlMean:    0.72,
		PValue:         0.02,
		Outcome:        "promoted",
		At:             metav1.Now(),
	}
	appendToolLineage(status, entry)
	if len(status.ToolLineage) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(status.ToolLineage))
	}
	if status.ToolLineage[0].ExperimentName != "exp-add-kubectl-exec" {
		t.Errorf("unexpected experiment name %q", status.ToolLineage[0].ExperimentName)
	}
}

func TestAppendToolLineage_Cap(t *testing.T) {
	status := &agentrollv1alpha1.AgentDeploymentStatus{}
	for i := 0; i < 22; i++ {
		appendToolLineage(status, agentrollv1alpha1.ToolLineageEntry{
			ExperimentName: fmt.Sprintf("exp-%d", i),
			Outcome:        "rejected",
		})
	}
	if len(status.ToolLineage) != 20 {
		t.Errorf("expected tool lineage capped at 20, got %d", len(status.ToolLineage))
	}
	// exp-0 and exp-1 evicted; exp-2 is now oldest surviving
	if status.ToolLineage[0].ExperimentName != "exp-2" {
		t.Errorf("expected oldest surviving entry exp-2, got %q", status.ToolLineage[0].ExperimentName)
	}
}
