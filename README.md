<p align="center">
  <h1 align="center">🎯 AgentRoll</h1>
  <p align="center">
    <strong>Kubernetes-native progressive delivery for AI agents in production</strong>
  </p>
  <p align="center">
    The missing layer between agent development frameworks and reliable production operations.
  </p>
  <p align="center">
    <a href="https://github.com/ywc668/agentroll/blob/main/LICENSE"><img src="https://img.shields.io/github/license/ywc668/agentroll" alt="License"></a>
    <a href="https://github.com/ywc668/agentroll/stargazers"><img src="https://img.shields.io/github/stars/ywc668/agentroll" alt="Stars"></a>
    <a href="https://github.com/ywc668/agentroll/issues"><img src="https://img.shields.io/github/issues/ywc668/agentroll" alt="Issues"></a>
    <a href="https://github.com/ywc668/agentroll"><img src="https://img.shields.io/badge/status-alpha-orange" alt="Status"></a>
  </p>
</p>

---

## The Problem

AI agent frameworks (LangGraph, CrewAI, OpenAI Agents SDK) help you **build** agents. Cloud platforms help you **run** them. But nothing helps you **safely ship changes** to agents already in production.

Today, most teams deploy agents the same way they deploy microservices — `docker push` then pray. But agents are fundamentally different:

- **4 layers change simultaneously**: prompt, model version, tool configurations, and memory — a 2-word prompt change can break production
- **Non-deterministic behavior**: the same input can trigger different tool calls and reasoning paths every time
- **No meaningful unit tests**: traditional pass/fail assertions don't work when outputs vary per run
- **Unpredictable costs**: one agent task can consume 10x-100x more tokens than another
- **Rollback is structurally harder**: stateful agents modify external systems (databases, APIs, emails) that can't be simply reverted

**The result?** 70% of regulated enterprises rebuild their agent stack every 3 months. Teams manually eyeball evaluation results. Nobody knows if the new version is actually better until users complain.

Read more: [Why AI Agents Need Their Own Deployment Infrastructure](https://dev.to/ywc668/why-ai-agents-need-their-own-deployment-infrastructure) (blog post)

## The Solution

AgentRoll brings **evaluation-gated progressive delivery** to AI agent deployments on Kubernetes. Think of it as [Argo Rollouts](https://argoproj.github.io/rollouts/) meets agent-aware intelligence.

```
                    ┌─────────────┐
                    │  New Agent   │
                    │  Version     │
                    └──────┬──────┘
                           │
                    ┌──────▼──────┐
                    │  5% Canary  │──── Eval: hallucination rate, tool success,
                    │             │     cost-per-task, latency
                    └──────┬──────┘
                           │ ✅ Pass
                    ┌──────▼──────┐
                    │ 20% Canary  │──── Eval: same metrics, larger sample
                    │             │
                    └──────┬──────┘
                           │ ✅ Pass
                    ┌──────▼──────┐
                    │ 50% Canary  │──── Eval: cost comparison vs baseline
                    │             │
                    └──────┬──────┘
                           │ ✅ Pass
                    ┌──────▼──────┐
                    │ 100% Stable │
                    │             │
                    └─────────────┘

            ❌ Any step fails → automatic rollback
```

## Key Features

> ⚠️ **AgentRoll is in early alpha.** We're building in public — contributions and feedback welcome.

| Feature | Description | Status |
|---------|-------------|--------|
| **AgentDeployment CRD** | Declare your agent's complete deployable config as a Kubernetes custom resource | ✅ Done |
| **Composite Version Tracking** | Track prompt + model + image tag as a single versioned entity via Pod labels | ✅ Done |
| **Argo Rollouts Integration** | Automatic translation of AgentDeployment to Argo Rollout with canary steps | ✅ Done |
| **Evaluation-Gated Canary** | Quality gates block bad canaries — response length, latency, tool usage, content quality | ✅ Done |
| **3-Layer AnalysisTemplate** | Pre-built defaults, user override, or fully custom — opinionated defaults with full escape hatches | ✅ Done |
| **Auto Service Creation** | Automatic Kubernetes Service creation when agent exposes ports | ✅ Done |
| **Bad Canary Demo** | End-to-end demo: degraded agent detected and rolled back automatically | ✅ Done |
| **Langfuse Integration** | Agent trace data as canary analysis source (tool success rate gate) | 🔨 In Progress |
| **OTel Observability** | Auto-injected OpenTelemetry sidecar for agent tracing | ⚠️ Sidecar ready, dashboard pending |
| **Grafana Dashboards** | Pre-built dashboards for agent-specific metrics | 📋 Planned |
| **Cost-Aware Scaling** | KEDA-based autoscaling with queue-depth metrics and token budgets | 🗓️ Future |
| **MCP Tool Lifecycle** | Manage MCP tool server versions alongside agents | 🗓️ Future |
| **Multi-Agent Coordination** | Coordinated canary deployments across dependent agents | 🗓️ Future |

## Architecture

```
┌────────────────────────────────────────────────────────────┐
│                      User Interface                        │
│           kubectl  /  Helm  /  ArgoCD  /  CI/CD            │
└──────────────────────────┬─────────────────────────────────┘
                           │
┌──────────────────────────▼─────────────────────────────────┐
│                   AgentRoll Operator                        │
│                                                            │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│  │    CRD       │  │   Rollout    │  │  AnalysisTemplate│ │
│  │  Controller  │  │   Manager    │  │    Manager       │ │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘ │
│         │                 │                    │           │
│         ▼                 ▼                    ▼           │
│  AgentDeployment    Argo Rollouts       3-Layer Template   │
│  CRD               (canary engine)     (default/override/ │
│                                         custom)           │
└──────────────────────────┬─────────────────────────────────┘
                           │
┌──────────────────────────▼─────────────────────────────────┐
│                   Kubernetes Cluster                        │
│                                                            │
│  ┌────────────┐  ┌────────────┐  ┌──────────────────────┐ │
│  │ Agent Pod  │  │ Agent Pod  │  │  Composite Version   │ │
│  │ v1 (stable)│  │ v2 (canary)│  │  Labels on each Pod  │ │
│  └────────────┘  └────────────┘  └──────────────────────┘ │
│                                                            │
│  Labels: agentroll.dev/prompt-version=v1                   │
│          agentroll.dev/model-version=claude-sonnet-4       │
│          agentroll.dev/composite-version=v1.claude-sonnet..│
└────────────────────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

- Kubernetes cluster (kind, minikube, or remote)
- [Argo Rollouts](https://argoproj.github.io/argo-rollouts/installation/) installed on the cluster
- kubectl configured

### Install and Run

```bash
# Clone the repo
git clone https://github.com/ywc668/agentroll.git
cd agentroll

# Install CRD to your cluster
make install

# Run the operator locally (development mode)
make run
```

### Deploy Your First Agent

In a separate terminal:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: my-agent
  namespace: default
spec:
  replicas: 2
  container:
    image: nginx:latest
    ports:
      - containerPort: 80
        name: http
  agentMeta:
    promptVersion: "v1"
    modelVersion: "claude-sonnet-4"
    modelProvider: "anthropic"
  rollout:
    strategy: canary
    steps:
      - setWeight: 20
        pause: { duration: "30s" }
        analysis: { templateRef: agent-quality-check }
      - setWeight: 50
        pause: { duration: "30s" }
      - setWeight: 100
EOF
```

### Verify

```bash
# See your AgentDeployment with composite version
kubectl get agentdeployments
# NAME       PHASE    STABLE                      CANARY   WEIGHT   AGE
# my-agent   Stable   v1.claude-sonnet-4.latest            0        30s

# See the Argo Rollout (not a plain Deployment!)
kubectl get rollouts

# See composite version labels on pods
kubectl get pods --show-labels

# See auto-created Service
kubectl get services
```

## Try the Bad Canary Demo

The fastest way to see AgentRoll's quality gates in action. No external services needed.

```bash
# 1. Start a Kind cluster with Argo Rollouts + AgentRoll operator
kind create cluster --name agentroll-demo
kubectl create namespace argo-rollouts
kubectl apply -n argo-rollouts \
  -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml
make install && make deploy IMG=controller:latest

# 2. Build the example agent (includes a "degraded" prompt variant)
cd examples/k8s-health-agent
docker build -t k8s-health-agent:v1 .
docker build -t agentroll-analysis:v1 analysis/
kind load docker-image k8s-health-agent:v1 agentroll-analysis:v1 --name agentroll-demo

# 3. Deploy prerequisites
kubectl apply -f k8s/rbac.yaml
kubectl create secret generic llm-credentials \
  --from-literal=anthropic-api-key=<YOUR_KEY>

# 4. Deploy stable version, then trigger a bad canary
kubectl apply -f k8s/agent-deployment.yaml   # stable: v4 prompt, uses tools
kubectl apply -f k8s/bad-canary-demo.yaml    # canary: degraded-v2, no tools

# 5. Watch the quality gate catch it
kubectl argo rollouts get rollout k8s-health-agent --watch
```

**What you'll see**: The canary (`degraded-v2`) produces responses with 0 tool calls.
The analysis runner detects this, marks the AnalysisRun as Failed, and Argo Rollouts
automatically rolls back to the stable version. The stable pods never go down.

See [`examples/k8s-health-agent/`](examples/k8s-health-agent/) for full details.

## AgentDeployment CRD

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: customer-support-agent
spec:
  # Framework-agnostic: works with LangGraph, CrewAI, OpenAI Agents SDK, or any container
  container:
    image: myregistry/support-agent:v2.1.0
    env:
      - name: LLM_PROVIDER
        value: anthropic
      - name: LLM_MODEL
        value: claude-sonnet-4-20250514

  # The 4-layer composite version — what makes agents different from microservices
  agentMeta:
    promptVersion: "abc123"          # Git commit ref
    modelVersion: "claude-sonnet-4-20250514"
    toolDependencies:
      - name: crm-mcp-server
        version: ">=1.2.0"

  # Progressive delivery with evaluation gates
  rollout:
    strategy: canary
    steps:
      - setWeight: 5
        pause: { duration: "5m" }
        analysis: { templateRef: agent-quality-check }   # Use built-in template
      - setWeight: 20
        pause: { duration: "10m" }
        analysis: { templateRef: my-custom-eval }         # Or bring your own
      - setWeight: 100

  # Auto-rollback on quality degradation or cost spike
  rollback:
    onFailedAnalysis: true
    onCostSpike:
      threshold: "200%"

  # Queue-depth scaling (not CPU — agents are I/O bound)
  scaling:
    minReplicas: 2
    maxReplicas: 10
    metric: queue-depth
    targetValue: 5
```

## AnalysisTemplate: 3-Layer Design

AgentRoll uses a principled approach to evaluation templates:

| Layer | Behavior | Example |
|-------|----------|---------|
| **Managed default** | AgentRoll auto-creates templates like `agent-quality-check` with sensible defaults | Zero config needed |
| **User override** | Create your own template with the same name (without `managed-by: agentroll` label) — AgentRoll won't overwrite it | Full control, familiar name |
| **Fully custom** | Reference any template name — AgentRoll assumes you manage it entirely | Maximum flexibility |

**Philosophy: opinionated defaults, full escape hatches.**

## Why Not Just Use...?

| Tool | What it does well | What it doesn't do |
|------|-------------------|-------------------|
| **Argo Rollouts** | Progressive delivery for any K8s workload | Doesn't understand agent health metrics (hallucination rate, tool success, cost-per-task) |
| **LangSmith Deploy** | Deep LangGraph integration | Commercial license required; LangGraph only; no progressive delivery |
| **Kagent** | K8s-native agent CRDs | Focused on SRE/DevOps agents, not general agent deployment lifecycle |
| **AWS AgentCore** | Fully managed agent runtime | Vendor lock-in; no progressive delivery; not open-source |
| **Plain K8s Deployment** | Simple, well-understood | No canary, no eval gates, no agent-aware rollback |

**AgentRoll** = Argo Rollouts' progressive delivery engine + agent-aware quality signals + framework-agnostic design.

## Roadmap

- **Phase 0** ✅ — Project setup, CRD design, community foundation
- **Phase 1 Sprint 1** ✅ — Core controller: AgentDeployment → Rollout + Service
- **Phase 1 Sprint 2** ✅ — Argo Rollouts integration with canary strategy + 3-layer AnalysisTemplate
- **Phase 1 Sprint 2.5** ✅ — Dogfooding: `k8s-health-agent` with OTel sidecar + real analysis runner
- **Phase 1 Sprint 3** 🔨 — Quality gates validated end-to-end; Langfuse integration in progress
- **Phase 2** 📋 — Production hardening: Grafana dashboards, KEDA scaling, Terraform modules
- **Phase 3** 🗓️ — Ecosystem: MCP tool lifecycle, A2A coordination

See [docs/ROADMAP.md](docs/ROADMAP.md) for the detailed sprint plan.

## Contributing

We welcome contributions! AgentRoll is in its earliest stages — now is the best time to get involved and shape the project's direction.

- 🐛 [Report bugs](https://github.com/ywc668/agentroll/issues)
- 💡 [Request features](https://github.com/ywc668/agentroll/issues)
- 💬 [Join discussions](https://github.com/ywc668/agentroll/discussions)
- 📖 [Read contributing guide](CONTRIBUTING.md)

## Background

Read the full story behind AgentRoll:
- 📝 [Why AI Agents Need Their Own Deployment Infrastructure](https://dev.to/ywc668/why-ai-agents-need-their-own-deployment-infrastructure) — the problem definition
- 📐 [ADR-001: Build on Argo Rollouts](docs/adr/001-build-on-argo-rollouts.md) — why we extend rather than reinvent

## License

[MIT](LICENSE)

---

<p align="center">
  <sub>Built with ☕ and conviction that AI agents deserve the same deployment rigor as microservices.</sub>
</p>
