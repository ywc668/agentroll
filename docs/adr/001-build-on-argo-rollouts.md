# ADR-001: Build on Argo Rollouts instead of implementing progressive delivery from scratch

## Status
Accepted

## Date
2026-03-22

## Context
AgentRoll needs progressive delivery capabilities (canary deployments, traffic splitting, automated analysis, rollback). There are two approaches:

1. **Build on Argo Rollouts**: Create an operator that translates `AgentDeployment` CRDs into Argo `Rollout` resources with agent-specific `AnalysisTemplate` configurations.

2. **Build from scratch**: Implement our own progressive delivery engine using Kubernetes Deployments, Services, and custom traffic splitting.

## Decision
We will build on top of Argo Rollouts.

## Rationale

**Engineering effort**: Implementing progressive delivery from scratch requires traffic splitting (service mesh or ingress controller integration), metric collection and analysis, rollback orchestration, and UI/CLI tooling. This is 6-12 months of work for a team. Argo Rollouts has 8+ years of development and is battle-tested in thousands of production clusters.

**Community and ecosystem**: Argo Rollouts is a CNCF graduated project with broad adoption. Building on it means AgentRoll users get compatibility with existing ArgoCD workflows, Ingress/Service Mesh integrations (Istio, Nginx, ALB), and a large community for support.

**Our unique value is the agent-aware layer, not the delivery engine**: AgentRoll's differentiation is understanding agent health metrics (hallucination rate, tool success rate, cost-per-task) and using them as promotion/rollback signals. This is the Analysis layer, not the traffic splitting layer.

**Reduced scope = faster time to value**: By leveraging Argo Rollouts, our MVP can focus entirely on the agent-specific intelligence — the part that no existing tool provides.

## Consequences

**Positive**:
- MVP can be built in 6-8 weeks instead of 6+ months
- Inherits Argo Rollouts' maturity, traffic splitting, and UI
- Users with existing Argo workflows can adopt incrementally
- We can contribute agent-specific AnalysisProviders back to the Argo community

**Negative**:
- Hard dependency on Argo Rollouts (must be installed in cluster)
- Constrained by Argo Rollouts' extension points (AnalysisTemplate, Metric Providers)
- Cannot diverge from Argo's progressive delivery model if agent needs differ significantly

**Mitigations**:
- Abstract the Argo integration behind an interface so we can swap engines in the future
- Engage early with the Argo community to understand extension point limitations
- If Argo's model proves insufficient, Phase 3+ can explore a standalone mode

## References
- [Argo Rollouts Architecture](https://argoproj.github.io/argo-rollouts/architecture/)
- [Argo Rollouts Analysis](https://argoproj.github.io/argo-rollouts/features/analysis/)
- [Kagent's approach](https://kagent.dev/) — uses CRDs but no progressive delivery
