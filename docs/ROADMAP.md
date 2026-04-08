# AgentRoll Roadmap

This document tracks near-term sprint goals. For the long-term vision (v1–v4 continuous
learning loop), see [ADR-002](adr/002-continuous-learning-vision.md).

---

## Sprint 3 — Observability & Credible Quality Gates

### Completed (P1 + P2)

---

## Sprint 4 — Production Hardening (complete)

All Sprint 4 items delivered.

---

## Sprint 5 — Ecosystem Expansion (complete)

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
| 3 P1 | Langfuse e2e: agent traces tagged with `canary:{version}`, `langfuse_metrics.py` queries real data, `agent-langfuse-check` AnalysisTemplate gates canary on `tool_success_rate >= 90%` |
| 3 P1 | Controller injects `canary-version` arg into every analysis step for Langfuse filtering |
| 3 P1 | `docs/langfuse/docker-compose.yml` — headless Langfuse v2 local setup for Kind |
| 3 P1 | `docs/langfuse/SETUP.md` — Langfuse setup guide for self-hosted and cloud.langfuse.com |
| 3 P2 | E2E test: bad canary rejection flow (`test/e2e/e2e_test.go` — always-fail AnalysisTemplate + rollback assertion) |
| 3 P2 | Makefile `setup-test-e2e` installs Argo Rollouts into the test Kind cluster |
| 3 P1 | OTel → Prometheus → Grafana pipeline: `agent.py` emits OTLP metrics (request counter, duration histogram, token counter, tool call counter); OTel sidecar config adds `prometheus` exporter on port 8889; `config/prometheus/agent-pod-monitor.yaml` PodMonitor scrapes all agent pods |
| 3 P1 | Additional Langfuse metrics: `avg_latency` (avg/p95 from trace latency field) and `token_cost_ratio` (per-trace cost canary vs stable) added to `langfuse_metrics.py`; `agent-langfuse-check.yaml` updated with `metric` arg to switch between all three metrics |
| 3 P2 | `onCostSpike` enforcement: controller auto-injects `agent-cost-check` analysis step when `spec.rollback.onCostSpike` is set; `agent-cost-check` managed template implemented using `langfuse_metrics.py token_cost_ratio`; threshold parsed from `"200%"` format |
| 3 P2 | Finalizer: controller adds `agentroll.dev/finalizer` to all AgentDeployments and explicitly deletes owned Argo Rollout on deletion to prevent orphaned resources |
| 4 | Terraform modules: `terraform/modules/{kind-cluster,argo-rollouts,agentroll,langfuse}` + `terraform/environments/local/` — one `terraform apply` bootstraps a full dev cluster |
| 4 | Multi-framework examples: `examples/{langgraph-agent,crewai-agent,autogen-agent}` — each with agent.py, Dockerfile, requirements.txt, and AgentDeployment k8s manifest |
| 4 | KEDA ScaledObject generation: `reconcileScaledObject()` creates KEDA ScaledObjects for redis/rabbitmq/sqs when `spec.scaling.queueRef` is set; gracefully skips if KEDA not installed |
| 4 | RBAC hardening: `reconcileServiceAccount()` auto-creates a dedicated SA per agent when `spec.serviceAccountName` is unset; KEDA + ServiceAccount RBAC markers added |
| 5 | MCP tool lifecycle: `reconcileToolDependencies()` resolves MCP endpoints via K8s Service discovery, validates semver constraints from `spec.agentMeta.toolDependencies`, injects `MCP_TOOL_<NAME>_ENDPOINT` env vars, blocks rollout on unmet constraints |
| 5 | A2A multi-agent coordination: `spec.dependsOn []string` field + `checkAgentDependencies()` in controller; requeues every 30s until all dependency AgentDeployments reach Stable phase |
| 5 | Hallucination rate signal: `hallucination_rate` metric in `langfuse_metrics.py` reads Langfuse Scores (`hallucination` > 0.5 OR `factuality` < 0.5 flags a trace); `agent-langfuse-check.yaml` exposes `max-hallucination-rate` arg (default 0.10) |
