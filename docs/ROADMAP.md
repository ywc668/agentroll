# AgentRoll Roadmap

This document tracks sprint goals and production-readiness work.
For the long-term vision (v1–v4 continuous learning loop), see [ADR-002](adr/002-continuous-learning-vision.md).

---

## Sprint 6 — Production Readiness ✅ COMPLETE

_Goal: make AgentRoll installable, observable, and safe to run in production._

### Critical (blocks adoption)

| # | Item | Status |
|---|------|--------|
| 6.1 | **Release pipeline** — goreleaser + ghcr.io multi-arch image push, Helm chart OCI publish, GitHub Releases with changelog | ✅ Done |
| 6.2 | **Kubernetes Events** — emit recorder events on all state transitions (`Progressing`, `Degraded`, `RollingBack`, errors) so `kubectl describe` is useful | ✅ Done |
| 6.3 | **Status conditions** — wire `meta.SetStatusCondition()` for `Available`, `Progressing`, `Degraded` so ArgoCD/Flux/`kubectl wait` can gate on them | ✅ Done |

### High (needed before GA)

| # | Item | Status |
|---|------|--------|
| 6.4 | **Leader election on by default** + `PodDisruptionBudget` for controller HA | ✅ Done |
| 6.5 | **RBAC least-privilege audit** — enumerate exact verbs per resource group, remove cluster-wide secret read | ✅ Done |
| 6.6 | **Security scanning in CI** — `govulncheck`, `trivy` image scan, `cosign` signing on releases | ✅ Done |

### Medium (operator maturity)

| # | Item | Status |
|---|------|--------|
| 6.7 | **Upgrade path documentation** — CRD migration guide, `make upgrade-crds` target, compatibility matrix | ✅ Done |
| 6.8 | **Reconciler reliability** — `MaxConcurrentReconciles=4`, differentiated backoff for transient vs permanent errors | ✅ Done |
| 6.9 | **`values.schema.json`** for Helm chart — enables `helm lint` value validation | ✅ Done |
| 6.10 | **API reference docs** — `docs/API_REFERENCE.md` with all fields, types, examples | ✅ Done |

### Lower priority (polish)

| # | Item | Status |
|---|------|--------|
| 6.11 | Prometheus AlertRules for the controller itself (reconcile errors, degraded/stuck agents) | ✅ Done |
| 6.12 | Test coverage ≥ 70% (35 new unit tests; controller: 50% → 70%) | ✅ Done |
| 6.13 | ArtifactHub listing for Helm chart | ✅ Done |
| 6.14 | `SECURITY.md` + responsible disclosure process | ✅ Done |
| 6.15 | cosign image signing + SBOM generation in release pipeline | ✅ Done |

---

## Sprint 7 — Self-Evolution (Planned)

_Goal: close the feedback loop — use AgentRoll's own quality signals to improve agents._

### Concept

AgentRoll already collects rich signals per rollout (Langfuse scores, latency, cost, hallucination rate).
Sprint 7 closes the loop: those signals drive the _next_ iteration of the agent automatically.

```
AgentDeployment (v_n)
    ↓ canary rollout
AnalysisRun (Langfuse scores: tool success, latency, cost, hallucination)
    ↓ gate fails OR score trends down
Evolution controller reads traces → proposes v_{n+1}
    ↓
New AgentDeployment (auto-created or PR opened for human approval)
```

### Planned Items

| # | Item | Mode |
|---|------|------|
| 7.1 | **`spec.evolution` CRD extension** — `enabled`, `strategy`, `trigger`, `optimizer`, `humanApproval` fields | CRD |
| 7.2 | **Threshold tuner** — adjusts `maxCostRatio`, `maxHallucinationRate` gates based on rolling baseline from historical `AnalysisRun` outcomes | No LLM required |
| 7.3 | **Prompt optimizer** — reads failing trace content from Langfuse, calls LLM to suggest prompt rewrites, opens a PR | LLM-assisted |
| 7.4 | **Model upgrader** — proposes model version bump when quality plateaus and a newer model is available | LLM-assisted |

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
| 3 P1 | Langfuse e2e: agent traces tagged with `canary:{version}`, queries real data, gates canary on `tool_success_rate >= 90%` |
| 3 P1 | Controller injects `canary-version` arg into every analysis step for Langfuse filtering |
| 3 P1 | `docs/langfuse/` — Langfuse setup guide + docker-compose for local dev |
| 3 P1 | OTel → Prometheus → Grafana pipeline: OTLP metrics, prometheus exporter on :8889, PodMonitor |
| 3 P1 | Additional Langfuse metrics: `avg_latency`, `token_cost_ratio` |
| 3 P2 | `onCostSpike` enforcement: auto-inject `agent-cost-check` analysis step |
| 3 P2 | Finalizer: explicit Rollout deletion on AgentDeployment delete |
| 3 P2 | E2E test: bad canary rejection flow (always-fail AnalysisTemplate + rollback assertion) |
| 4 | Terraform modules: one `terraform apply` bootstraps full dev cluster (Kind + Argo + Langfuse + AgentRoll) |
| 4 | Multi-framework examples: LangGraph, CrewAI, AutoGen |
| 4 | KEDA ScaledObject generation for redis/rabbitmq/sqs |
| 4 | RBAC hardening: auto-create dedicated SA per agent |
| 5 | MCP tool lifecycle: semver-gated endpoint injection via K8s Service discovery |
| 5 | A2A multi-agent coordination: `spec.dependsOn`, 30s requeue until dependencies Stable |
| 5 | Hallucination rate signal via Langfuse Scores API |
| 6 | Validating webhook: admission-time rejection of invalid specs (5 rules, 14 tests) |
| 6 | Helm chart tests: `helm test` pod curls `/healthz` + `/readyz` |
| 6 | Helm chart webhook support: Service, ValidatingWebhookConfiguration, cert-manager Certificate |
| 6 | E2E test ordering fix: bad canary test nested inside Manager Describe to survive Ginkgo randomization |
| 6 | Release pipeline: goreleaser multi-arch, OCI Helm push, cosign signing, Trivy + govulncheck in CI |
| 6 | Kubernetes Events + status conditions (`Available`, `Progressing`, `Degraded`) |
| 6 | PodDisruptionBudget + RBAC least-privilege audit |
| 6 | Helm `values.schema.json`, ArtifactHub metadata, `SECURITY.md` |
| 6 | `docs/API_REFERENCE.md` + `docs/UPGRADE.md` + `make upgrade-crds` |
| 6 | Prometheus AlertRules for controller + agent rollout health |
| 6 | Test coverage 50% → 70% (35 new unit tests) |
| 6 | `MaxConcurrentReconciles=4` reconciler reliability |
