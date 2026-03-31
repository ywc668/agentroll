# AgentRoll

Kubernetes operator for AI agent progressive delivery, built on Argo Rollouts.

## Quick Reference

```bash
make manifests generate  # Regenerate CRDs + DeepCopy after changing types.go
make build               # Build controller binary
make test                # Run unit tests (requires envtest binaries)
make fmt vet lint        # Format, vet, lint
make docker-build        # Build container image
```

Always run `make manifests generate` after modifying `api/v1alpha1/agentdeployment_types.go`.

## Project Structure

```
api/v1alpha1/          # CRD types (AgentDeployment spec/status)
internal/controller/   # Reconciler: Rollout, AnalysisTemplate, Service, OTel ConfigMap
cmd/                   # Operator entrypoint
config/                # Kustomize manifests (CRDs, RBAC, samples)
charts/agentroll/      # Helm chart
examples/              # Dogfooding example (k8s-health-agent)
templates/analysis/    # Pre-built AnalysisTemplate library
test/                  # Unit tests (Ginkgo) + e2e tests + test CRDs
docs/                  # ADRs, blog posts, setup guides
dashboards/            # Grafana dashboard templates
```

## Key Concepts

- **AgentDeployment CRD** → translates to Argo Rollout + AnalysisTemplate + Service
- **Composite version**: `{promptVersion}.{modelVersion}.{imageTag}` tracked as pod label
- **3-layer AnalysisTemplate**: managed defaults (agent-quality-check, agent-cost-check) → user override via templateRef → fully custom (controller skips if no managed label)
- **Analysis runner**: Python Job in `examples/k8s-health-agent/analysis/runner.py` — checks health, query quality, latency, cost

## Dependencies

- Go 1.25, controller-runtime v0.23, Argo Rollouts v1.9
- Testing: Ginkgo/Gomega + envtest (auto-downloaded)
- Argo CRDs for tests live in `test/crds/`

## Code Conventions

- Kubebuilder scaffolding — follow existing RBAC markers and reconciler patterns
- Controller reconcile flow: fetch → build composite version → reconcile AnalysisTemplate → OTel ConfigMap → Rollout → Service → status
- Tests use Ginkgo BDD style (`Describe/Context/It`)
