# AgentRoll Roadmap

This document tracks near-term sprint goals. For the long-term vision (v1–v4 continuous
learning loop), see [ADR-002](adr/002-continuous-learning-vision.md).

---

## Sprint 3 — Observability & Credible Quality Gates

### P0 (must ship)

- **Bad canary demo**: prove quality gates actually block a degraded agent. Adds a
  `degraded-v2` prompt variant to `k8s-health-agent` and fixes the `runner.py`
  content_quality override bug. Demonstrates the full reject-and-rollback flow.
- **Langfuse metric provider (minimum viable)**: replace the placeholder
  `agent-quality-check.yaml` with a real `agent-langfuse-check.yaml` that queries
  Langfuse traces and computes tool success rate as a gate signal.

### P1

- Langfuse SDK instrumentation in `k8s-health-agent` (tag traces with composite version)
- Grafana dashboard panels wired to OTel metrics from the sidecar
- `runner.py` content quality improvements — configurable severity
- Documentation: end-to-end bad-canary-demo walkthrough

### P2

- Additional Langfuse metrics: avg latency, token consumption ratio vs stable
- E2E test that asserts canary rejection flow (Kind cluster + real AnalysisRun)
- `onCostSpike` enforcement in the controller
- Finalizer for orphaned Argo Rollout cleanup

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
