/*
Copyright 2026 AgentRoll Contributors.
Licensed under the MIT License.
*/

package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newTestAD returns a minimal valid AgentDeployment and applies any mutations.
func newTestAD(name string, mutate func(*AgentDeploymentSpec)) *AgentDeployment {
	ad := &AgentDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: AgentDeploymentSpec{
			Container: AgentContainerSpec{Image: "busybox:latest"},
			Rollout:   RolloutSpec{Strategy: "canary"},
		},
	}
	if mutate != nil {
		mutate(&ad.Spec)
	}
	return ad
}

var validator = &AgentDeploymentCustomValidator{}
var ctx = context.Background()

// ─── cost spike ───────────────────────────────────────────────────────────────

func TestValidateCostSpike_Valid(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Rollback = &RollbackSpec{OnCostSpike: &CostSpikeSpec{Threshold: "200%"}}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateCostSpike_MissingPercent(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Rollback = &RollbackSpec{OnCostSpike: &CostSpikeSpec{Threshold: "200"}}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for missing %, got nil")
	}
}

func TestValidateCostSpike_TooLow(t *testing.T) {
	for _, thresh := range []string{"100%", "50%", "0%"} {
		ad := newTestAD("a", func(s *AgentDeploymentSpec) {
			s.Rollback = &RollbackSpec{OnCostSpike: &CostSpikeSpec{Threshold: thresh}}
		})
		_, err := validator.ValidateCreate(ctx, ad)
		if err == nil {
			t.Fatalf("expected error for threshold %q, got nil", thresh)
		}
	}
}

func TestValidateCostSpike_NotNumeric(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Rollback = &RollbackSpec{OnCostSpike: &CostSpikeSpec{Threshold: "lots%"}}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for non-numeric threshold, got nil")
	}
}

// ─── scaling ──────────────────────────────────────────────────────────────────

func TestValidateScaling_Valid_CPU(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Scaling = &ScalingSpec{MinReplicas: 1, MaxReplicas: 5, Metric: "cpu", TargetValue: 80}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateScaling_MaxLessThanMin(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Scaling = &ScalingSpec{MinReplicas: 5, MaxReplicas: 2, Metric: "cpu"}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for maxReplicas < minReplicas, got nil")
	}
}

func TestValidateScaling_QueueDepth_MissingQueueRef(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Scaling = &ScalingSpec{MinReplicas: 1, MaxReplicas: 5, Metric: "queue-depth"}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for missing queueRef, got nil")
	}
}

func TestValidateScaling_QueueDepth_WithQueueRef(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Scaling = &ScalingSpec{
			MinReplicas: 1,
			MaxReplicas: 5,
			Metric:      "queue-depth",
			QueueRef:    &QueueReference{Provider: "redis", Address: "redis:6379", QueueName: "tasks"},
		}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ─── tool dependencies ────────────────────────────────────────────────────────

func TestValidateToolDependencies_Valid(t *testing.T) {
	for _, ver := range []string{">=1.2.0", "~1.3", "^2.0.0", "1.x", ""} {
		ad := newTestAD("a", func(s *AgentDeploymentSpec) {
			s.AgentMeta.ToolDependencies = []ToolDependency{{Name: "crm", Version: ver}}
		})
		_, err := validator.ValidateCreate(ctx, ad)
		if err != nil {
			t.Fatalf("expected no error for version %q, got: %v", ver, err)
		}
	}
}

func TestValidateToolDependencies_Invalid(t *testing.T) {
	ad := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.AgentMeta.ToolDependencies = []ToolDependency{{Name: "crm", Version: "not-a-semver-!!!"}}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for invalid semver constraint, got nil")
	}
}

// ─── self dependency ──────────────────────────────────────────────────────────

func TestValidateDependsOn_SelfReference(t *testing.T) {
	ad := newTestAD("my-agent", func(s *AgentDeploymentSpec) {
		s.DependsOn = []string{"my-agent"}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err == nil {
		t.Fatal("expected error for self-dependency, got nil")
	}
}

func TestValidateDependsOn_ValidReference(t *testing.T) {
	ad := newTestAD("my-agent", func(s *AgentDeploymentSpec) {
		s.DependsOn = []string{"other-agent"}
	})
	_, err := validator.ValidateCreate(ctx, ad)
	if err != nil {
		t.Fatalf("expected no error for valid dependency, got: %v", err)
	}
}

// ─── ValidateUpdate ───────────────────────────────────────────────────────────

func TestValidateUpdate_PropagatesValidation(t *testing.T) {
	old := newTestAD("a", nil)
	updated := newTestAD("a", func(s *AgentDeploymentSpec) {
		s.Rollback = &RollbackSpec{OnCostSpike: &CostSpikeSpec{Threshold: "bad"}}
	})
	_, err := validator.ValidateUpdate(ctx, old, updated)
	if err == nil {
		t.Fatal("expected ValidateUpdate to propagate validation errors, got nil")
	}
}

// ─── ValidateDelete ───────────────────────────────────────────────────────────

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	ad := newTestAD("a", nil)
	_, err := validator.ValidateDelete(ctx, ad)
	if err != nil {
		t.Fatalf("expected no error on delete, got: %v", err)
	}
}
