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

> ⚠️ **AgentRoll is in early alpha.** We're building in public. Features below represent our roadmap — check the status column for current availability.

| Feature | Description | Status |
|---------|-------------|--------|
| **AgentDeployment CRD** | Declare your agent's complete deployable config as a Kubernetes custom resource | 🔨 Building |
| **Evaluation-Gated Canary** | Progressive rollout with agent-quality gates (hallucination rate, tool success rate, cost-per-task) | 🔨 Building |
| **Argo Rollouts Integration** | Built on top of Argo Rollouts — not reinventing the wheel | 🔨 Building |
| **Agent AnalysisTemplates** | Pre-built quality metric templates for common agent patterns | 📋 Planned |
| **Langfuse Integration** | Out-of-the-box agent trace data as canary analysis source | 📋 Planned |
| **OTel Observability** | Auto-injected OpenTelemetry sidecar for agent tracing | 📋 Planned |
| **Grafana Dashboards** | Pre-built dashboards for agent-specific metrics | 📋 Planned |
| **Composite Versioning** | Track prompt + model + tools + memory as a single versioned entity | 📋 Planned |
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
│  │    CRD       │  │   Rollout    │  │    Analysis      │ │
│  │  Controller  │  │   Manager    │  │    Engine        │ │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘ │
│         │                 │                    │           │
│         ▼                 ▼                    ▼           │
│  AgentDeployment    Argo Rollouts       Langfuse / OTel   │
│  CRD               (rollout engine)     (data sources)    │
└──────────────────────────┬─────────────────────────────────┘
                           │
┌──────────────────────────▼─────────────────────────────────┐
│                   Kubernetes Cluster                        │
│                                                            │
│  ┌────────────┐  ┌────────────┐  ┌──────────────────────┐ │
│  │ Agent Pod  │  │ Agent Pod  │  │  OTel Sidecar        │ │
│  │ v1 (stable)│  │ v2 (canary)│  │  (per pod)           │ │
│  └────────────┘  └────────────┘  └──────────────────────┘ │
│                                                            │
│  ┌──────────────────────────────────────────────────────┐ │
│  │  Prometheus  /  Grafana  /  Langfuse                 │ │
│  │  (agent metrics collection & visualization)          │ │
│  └──────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────┘
```

## Quick Start

> 🚧 Coming soon. AgentRoll is currently in active development.

```bash
# Install AgentRoll operator (coming soon)
helm repo add agentroll https://ywc668.github.io/agentroll
helm install agentroll agentroll/agentroll-operator

# Deploy your first agent with progressive delivery
kubectl apply -f examples/basic-agent-deployment.yaml
```

## AgentDeployment CRD (Preview)

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: customer-support-agent
spec:
  container:
    image: myregistry/support-agent:v2.1.0
    env:
      - name: LLM_PROVIDER
        value: anthropic
      - name: LLM_MODEL
        value: claude-sonnet-4-20250514

  agentMeta:
    promptVersion: "abc123"
    modelVersion: "claude-sonnet-4-20250514"
    toolDependencies:
      - name: crm-mcp-server
        version: ">=1.2.0"

  rollout:
    strategy: canary
    steps:
      - setWeight: 5
        pause: { duration: 5m }
        analysis:
          templateRef: agent-quality-check
      - setWeight: 20
        pause: { duration: 10m }
        analysis:
          templateRef: agent-quality-check
      - setWeight: 100

  rollback:
    onFailedAnalysis: true
    onCostSpike:
      threshold: 200%

  observability:
    langfuse:
      endpoint: "https://langfuse.internal"
    opentelemetry:
      enabled: true

  scaling:
    minReplicas: 2
    maxReplicas: 10
    metric: queue-depth
    targetValue: 5
```

## Why Not Just Use...?

| Tool | What it does well | What it doesn't do |
|------|-------------------|-------------------|
| **Argo Rollouts** | Progressive delivery for any K8s workload | Doesn't understand agent health metrics (hallucination rate, tool success, cost-per-task) |
| **LangSmith Deploy** | Deep LangGraph integration | Commercial license required; LangGraph only; no progressive delivery |
| **Kagent** | K8s-native agent CRDs | Focused on SRE/DevOps agents, not general agent deployment lifecycle |
| **AWS AgentCore** | Fully managed agent runtime | Vendor lock-in; no progressive delivery; no open-source |
| **Plain K8s Deployment** | Simple, well-understood | No canary, no eval gates, no agent-aware rollback |

**AgentRoll** = Argo Rollouts' progressive delivery + agent-aware quality signals + framework-agnostic design.

## Roadmap

- **Phase 0 (Current)** — Project setup, CRD design, community foundation
- **Phase 1** — MVP: AgentDeployment CRD + Argo Rollouts integration + Langfuse analysis
- **Phase 2** — Production hardening: multi-framework validation, Terraform modules, security
- **Phase 3** — Ecosystem: MCP tool lifecycle, A2A coordination, KEDA scaling, multi-agent deployment

See our [detailed roadmap](docs/ROADMAP.md) for more information.

## Contributing

We welcome contributions! AgentRoll is in its earliest stages — now is the best time to get involved and shape the project's direction.

- 🐛 [Report bugs](https://github.com/ywc668/agentroll/issues)
- 💡 [Request features](https://github.com/ywc668/agentroll/issues)
- 💬 [Join discussions](https://github.com/ywc668/agentroll/discussions)
- 📖 [Read contributing guide](CONTRIBUTING.md)

## Background & Motivation

This project was born from real-world experience managing release orchestration for cloud infrastructure at scale, combined with deep research into the AI agent deployment landscape. Key observations:

- **57% of organizations** now have agents in production, but most deploy them like traditional microservices
- **70% of regulated enterprises** rebuild their agent stack every 3 months
- Agent frameworks assume you'll solve deployment yourself — because it's genuinely hard
- The CNCF ecosystem is actively embracing agent infrastructure (Kagent, Agent Sandbox, KubeCon Agentics Day 2026)
- No open-source tool treats agents as first-class deployable units with evaluation-gated progressive delivery

For a deep dive into the landscape research, see our [Architecture Decision Records](docs/adr/).

## License

[MIT](LICENSE)

---

<p align="center">
  <sub>Built with ☕ and conviction that AI agents deserve the same deployment rigor as microservices.</sub>
</p>
