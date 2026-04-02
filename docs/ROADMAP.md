# AgentRoll Roadmap

This document tracks near-term sprint goals. For the long-term vision (v1–v4 continuous
learning loop), see [ADR-002](adr/002-continuous-learning-vision.md).

---

## Sprint 3 — Observability & Credible Quality Gates

### In Progress

- **Langfuse end-to-end**: deploy Langfuse, verify agent trace instrumentation, run
  `langfuse_metrics.py` against real trace data, gate a canary deployment on
  `tool_success_rate >= 90%` using `agent-langfuse-check.yaml`

### P1 (next)

- Grafana dashboard panels wired to OTel sidecar metrics
- Additional Langfuse metrics: avg latency, token consumption ratio vs stable
- Documentation: Langfuse setup guide for self-hosted and cloud.langfuse.com

### P2

- E2E test that asserts canary rejection flow (Kind cluster + real AnalysisRun)
- `onCostSpike` enforcement in the controller
- Finalizer for orphaned Argo Rollout cleanup on AgentDeployment delete

---

## Sprint 4 — Production Hardening

- Terraform modules for cluster bootstrapping (Argo Rollouts + AgentRoll + Langfuse)
- Multi-framework validation: LangGraph, CrewAI, AutoGen dogfooding examples
- KEDA `ScaledObject` generation from `spec.scaling.queueRef`
- RBAC hardening: least-privilege per-agent service accounts

---

## Sprint 5 — Ecosystem Expansion

- MCP tool lifecycle management: version-gate tool dependency upgrades
- Multi-agent coordination (A2A): AgentDeployment inter-dependencies
- `agent-langfuse-check.yaml` expanded with hallucination rate signal

---

## Completed

| Sprint | Deliverable |
|--------|-------------|
| 0 | Project setup, ADRs, community scaffolding |
| 1 | CRD (AgentDeployment), controller, Argo Rollout + Service reconciliation |
| 2 | Canary step translation, AnalysisTemplate 3-layer design, status sync |
| 2.5 | `k8s-health-agent` dogfooding, OTel sidecar injection, real analysis runner |
| 3 P0 | Quality gates validated end-to-end on Kind: bad canary detected and rolled back |
| 3 P0 | `runner.py` content_quality bug fixed, `tool_usage` check added |
| 3 P0 | `langfuse_metrics.py` Job script + `agent-langfuse-check.yaml` template written |
| 3 P0 | `StableVersion` now reads from stable ReplicaSet labels (not current spec) |
| 3 P0 | Controller RBAC: added `apps/replicasets` get;list;watch permission |
| 3 P0 | Controller test coverage: 46% → 63% |
