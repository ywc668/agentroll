# AgentRoll: Multi-Agent Mesh Roadmap

> **Goal**: Make AgentRoll the Kubernetes-native control plane for networks of AI agents —
> bringing service mesh concepts (traffic management, circuit breaking, canary rollouts,
> observability) to agent-to-agent (A2A) and agent-to-tool (MCP) traffic.

---

## The Analogy That Grounds This

| Microservice mesh concept | AgentRoll equivalent |
|---|---|
| Service | AgentDeployment |
| Service mesh sidecar (Envoy) | Agent Gateway sidecar (MCP/A2A proxy) |
| Virtual Service / routing rules | AgentRoute (new CRD) |
| Service graph | AgentGraph (new CRD) |
| Canary deployment | Already exists — extend to graph level |
| Circuit breaker | Per-agent-hop circuit breaker in AgentRoute |
| Distributed tracing (Zipkin/Jaeger) | OTel GenAI spans across agent hops |
| Control plane (Istiod) | AgentRoll operator |
| Traffic policy | AgentPolicy (new CRD) |

---

## Where the Field Stands

### What exists today

**Protocols (wire layer)**:
- **Google A2A v0.3** (Linux Foundation): agent↔agent wire protocol. Agents advertise capabilities via "Agent Cards", route tasks via a registry, carry trace IDs. Twilio has already implemented latency-aware agent selection on top of it.
- **Anthropic MCP** (Agentic AI Foundation / Linux Foundation): agent→tool wire protocol. De facto standard with 97M monthly SDK downloads. AI gateways (Portkey, Kong, Apache APISIX) building MCP-aware proxies for governance and tracing.

**Gateway layer (LLM-level, not agent-graph-level)**:
- **Portkey / LiteLLM**: retries (5x exponential backoff), circuit breakers, weighted load balancing, composable fallbacks. Production-ready but operates at the LLM API call level, not agent-to-agent call level.
- **Kong AI Gateway / Agent Gateway**: semantic routing across models, A2A and MCP proxy support. Commercial, Nginx-based.
- **Solo.io Agent Gateway + Agent Mesh**: the closest true service-mesh-for-agents product. Rust-based data plane, A2A+MCP aware, composable with Envoy. Commercial.

**Orchestration frameworks**:
- **LangGraph**: graph-based, explicit state, checkpointing, human-in-the-loop. Used widely. No built-in resilience primitives between agents.
- **Microsoft Agent Framework (AutoGen + Semantic Kernel)**: best OTel integration. Enterprise-hardened. No traffic management.
- **Ray/KubeRay**: most K8s-native. Actor model = agents. Ray Serve = traffic-managed endpoints. Best option for large agent fleets today.

**Research**:
- **Cognitive Fabric Nodes** (arXiv:2604.03430): Istio-inspired semantic intermediaries for agent traffic. Research-only.
- **Mesh Memory Protocol** (arXiv:2604.19540): MCP+A2A unified semantic state layer for long-running multi-agent tasks.

### The genuine gap AgentRoll can fill

No existing tool provides:
1. **Progressive delivery at the graph level** — canary a new planner agent while keeping specialist agents stable
2. **Quality-gate rollback across an agent graph** — if Agent B's canary causes Agent A's quality to drop, rollback B
3. **Composite version tracking for a graph** — each node has `{prompt}.{model}.{image}`, the graph has a graph-level version
4. **K8s-operator-driven east-west traffic management** — not a gateway, but the control plane that configures and monitors agent-to-agent traffic

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    AgentRoll Control Plane                   │
│                    (Kubernetes Operator)                     │
│                                                              │
│   AgentGraph CRD ──────────────────────────────────────────┐ │
│   (topology, policies, canary strategy for the whole graph)│ │
│                                                            │ │
│   AgentRoute CRD ──────────────────────────────────────────┤ │
│   (per-edge routing rules: weights, circuit breaker, retry)│ │
└────────────────────────────────────────────────────────────┼─┘
                                                             │
              configures ↓                     configures ↓
┌───────────────────────────┐    ┌───────────────────────────┐
│  Agent A                  │    │  Agent B                  │
│  (AgentDeployment)        │    │  (AgentDeployment)        │
│                           │    │                           │
│  [stable pod]             │    │  [stable pod]             │
│  [canary pod]  ──A2A──▶   │    │  [canary pod]             │
│                           │    │                           │
│  [agent-proxy sidecar]    │    │  [agent-proxy sidecar]    │
│  - A2A traffic mgmt       │    │  - MCP tool proxy         │
│  - OTel span emission     │    │  - OTel span emission     │
│  - circuit breaker        │    │  - circuit breaker        │
└───────────────────────────┘    └───────────────────────────┘
```

---

## Roadmap

### Phase 1 (Sprints 14–15) — Agent Graph Topology

**Theme**: Model multi-agent systems as a first-class Kubernetes resource.

#### Sprint 14 — AgentGraph CRD

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentGraph
metadata:
  name: customer-support-pipeline
spec:
  nodes:
    - name: planner
      agentDeploymentRef: planner-agent
      role: orchestrator
    - name: kb-specialist
      agentDeploymentRef: kb-agent
      role: specialist
    - name: escalation-agent
      agentDeploymentRef: escalation-agent
      role: specialist
  edges:
    - from: planner
      to: kb-specialist
      protocol: a2a
    - from: planner
      to: escalation-agent
      protocol: a2a
  rollout:
    strategy: rolling          # roll one node at a time, in dependency order
    analysisRef: graph-quality-check
```

| Item | What it does |
|---|---|
| **14.1 AgentGraph CRD** | Defines nodes (AgentDeployment references), edges (A2A calls between them), and a graph-level rollout strategy. |
| **14.2 Dependency-ordered rollout** | When graph spec changes, controller upgrades agents in topological order (leaf nodes first, orchestrators last) — opposite of service mesh where you'd upgrade leaves last. Reason: specialists must be stable before orchestrators call them. |
| **14.3 Graph version** | `status.graphVersion`: a hash of all node composite versions. Changes when any node changes. |
| **14.4 Graph-level analysis** | `spec.rollout.analysisRef` runs an end-to-end quality check on the whole graph (input enters planner, output quality scored) during node canary. |

#### Sprint 15 — AgentRoute and Traffic Management

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentRoute
metadata:
  name: planner-to-kb
spec:
  from: planner-agent
  to: kb-agent
  trafficPolicy:
    retries:
      attempts: 3
      perTryTimeout: 30s
      retryOn: [connection-failure, 5xx, timeout]
    circuitBreaker:
      consecutiveFailures: 5
      interval: 60s
      baseEjectionTime: 30s
    timeout: 120s
  canary:
    weight: 20               # 20% of planner's calls to kb-agent go to kb-agent canary
    stableWeight: 80
```

| Item | What it does |
|---|---|
| **15.1 AgentRoute CRD** | Per-edge routing policy: retries, circuit breaker, timeouts — applied at the agent-proxy sidecar level. |
| **15.2 Edge-level canary** | When kb-agent has a canary, planner's calls are split 80/20 between stable and canary kb-agent. The controller manages this split without application-level changes. |
| **15.3 Agent-proxy sidecar injection** | Controller injects a lightweight Go-based sidecar proxy (not Envoy — too heavy) into agent pods. Proxy intercepts outbound A2A calls and applies AgentRoute policies. |
| **15.4 Circuit breaker condition** | When circuit breaker opens on an edge, set `CircuitOpen` condition on the AgentRoute. Controller pauses any in-flight canary on that edge's target agent. |

---

### Phase 2 (Sprints 16–17) — Protocol Integration (A2A + MCP)

**Theme**: Speak the standard protocols natively so any compliant agent works with AgentRoll.

#### Sprint 16 — A2A Protocol Support

| Item | What it does |
|---|---|
| **16.1 Agent Card generation** | Controller auto-generates an A2A Agent Card from the AgentDeployment spec (capabilities from tool list, endpoint from Service, version from composite version). Publishes it to a ClusterAgentRegistry (ConfigMap-backed or etcd). |
| **16.2 A2A service discovery** | Agent proxy sidecar resolves `agent://kb-agent` addresses to cluster-internal Service endpoints via the registry. No hardcoded URLs in agent code. |
| **16.3 Latency-aware routing** | Agent proxy tracks p50/p99 latency per target replica. When a canary replica is significantly slower than stable (>2x), automatically reduce its traffic weight regardless of AgentRoute settings. |
| **16.4 Trace propagation** | Agent proxy injects/extracts W3C trace context headers on all A2A calls. Span attributes follow OTel GenAI semantic conventions (`gen_ai.agent.name`, `gen_ai.agent.version`, `gen_ai.operation.name: delegate`). |

#### Sprint 17 — MCP Gateway

| Item | What it does |
|---|---|
| **17.1 MCP proxy in sidecar** | Agent proxy intercepts outbound MCP tool calls. Records tool name, input/output size, latency, success/failure as OTel spans. |
| **17.2 Tool-level circuit breaker** | If a specific MCP tool fails N consecutive times, agent proxy starts returning a structured error response instead of calling the tool. Agent degrades gracefully. Controller emits `ToolCircuitOpen` event. |
| **17.3 Tool version routing** | When a tool's MCP server has a canary version running, agent proxy routes N% of tool calls to the canary version. Enables tool-level progressive delivery. |
| **17.4 MCP governance** | `spec.toolPolicy`: allowlist/denylist of tool names the agent is permitted to call. Agent proxy enforces this at the network level, not the application level. |

---

### Phase 3 (Sprints 18–19) — Graph-Level Progressive Delivery

**Theme**: The hardest part — rolling out changes across a multi-agent graph safely.

#### Sprint 18 — Coordinated Graph Rollout

| Item | What it does |
|---|---|
| **18.1 GraphRollout strategy** | `spec.rollout.strategy: coordinated` — upgrade all nodes simultaneously at N% traffic, run graph-level analysis, promote all or rollback all together. |
| **18.2 Cross-agent analysis** | `agent-graph-quality-check` AnalysisTemplate: sends a test query to the graph entry point, traces the full path, scores end-to-end output quality with LLM-as-judge. Fails if any hop added latency > threshold or if output quality dropped. |
| **18.3 Blast radius containment** | If node B's canary degrades, automatically rollback B AND pause any other nodes' in-progress canaries — don't let a degraded graph continue rolling out. |
| **18.4 GraphRollout history** | `status.rolloutHistory`: last 20 graph-level rollouts with which nodes changed, graph quality before/after, and outcome. |

#### Sprint 19 — Graph Evolution

| Item | What it does |
|---|---|
| **19.1 Graph-level threshold tuner** | Aggregate Langfuse scores per graph edge (latency, quality) and tune per-edge timeout/retry thresholds independently. |
| **19.2 Node swap proposal** | Model upgrader extended to the graph level: if one node is identified as the bottleneck (lowest quality contribution to graph output), propose a model upgrade for that specific node via PR. |
| **19.3 Topology optimizer** | Periodic analysis: given current quality and latency per edge, suggest removing a node (if it adds no quality) or adding a specialist (if the planner is overloaded). Output as a PR against the AgentGraph spec. |
| **19.4 A2A dependency evolution** | If agent A is waiting on agent B for >P95 of its calls, and B is a bottleneck, automatically propose B's model/prompt upgrade in the evolution loop. |

---

### Phase 4 (Sprint 20) — Observability and Dashboard

**Theme**: Make the invisible visible — full graph tracing, latency budgets, cost attribution.

| Item | What it does |
|---|---|
| **20.1 Graph trace visualization** | Grafana dashboard showing the agent graph as a live topology map. Each edge colored by health (green/yellow/red). Click a node to see its canary status and quality history. |
| **20.2 Latency budget propagation** | `spec.latencyBudget: 10s` on AgentGraph propagates through edges: each hop has a budget proportional to its historical share. Agent proxy enforces per-hop deadlines via context cancellation. |
| **20.3 Cost attribution per node** | Token costs attributed per agent-hop from Langfuse traces. `status.costBreakdown` shows which node consumed what % of the graph's total token cost. |
| **20.4 Failure attribution** | When a graph-level quality check fails, controller performs automatic root cause analysis: which hop first degraded? Which node contributed most to the quality drop? Surfaced in `status.lastFailureAnalysis`. |

---

## Protocol Positioning

```
MCP  (agent → tool)
 │  AgentRoll wraps: tool circuit breaker, tool versioning,
 │  tool call tracing, tool policy enforcement
 │
A2A  (agent → agent)
 │  AgentRoll wraps: routing, retries, circuit breaking,
 │  canary weight management, latency-aware selection,
 │  trace propagation, composite version tracking
 │
AgentGraph (multi-agent topology)
    AgentRoll provides: coordinated progressive delivery,
    graph-level quality gates, blast radius containment,
    topology evolution proposals
```

---

## Competitive Positioning

| Capability | AgentRoll target | Solo.io Agent Mesh | Portkey/LiteLLM | Kong AI Gateway | Ray/KubeRay |
|---|---|---|---|---|---|
| K8s-native CRD-based | ✅ | Partial | ❌ | ❌ | ✅ |
| Progressive delivery (canary) | ✅ (graph level) | ❌ | ❌ | ❌ | ❌ |
| Quality-gate rollback | ✅ | ❌ | ❌ | ❌ | ❌ |
| A2A protocol support | ✅ (Sprint 16) | ✅ | ❌ | ✅ | ❌ |
| MCP proxy/governance | ✅ (Sprint 17) | ✅ | ❌ | ✅ | ❌ |
| Circuit breaker (LLM level) | ✅ (via Portkey integration) | ✅ | ✅ | ✅ | ❌ |
| Circuit breaker (agent-graph edge) | ✅ (Sprint 15) | ❌ | ❌ | ❌ | ❌ |
| Composite version tracking | ✅ | ❌ | ❌ | ❌ | ❌ |
| Open source / self-hostable | ✅ | ❌ (commercial) | ✅ | ❌ (commercial) | ✅ |
| OTel GenAI conventions | ✅ (already) | ✅ | Partial | Partial | ✅ |

**The moat**: Solo.io is the only real competitor on the service mesh front, but it's commercial and gateway-focused. AgentRoll's operator-driven progressive delivery with quality gates is genuinely novel in the open-source space.

---

## Key Research References

- A2A Protocol: [a2a-protocol.org](https://a2a-protocol.org/latest/) / [GitHub](https://github.com/a2aproject/A2A)
- MCP Spec November 2025: [modelcontextprotocol.io](https://modelcontextprotocol.io/specification/2025-11-25)
- Solo.io Agent Gateway launch: [solo.io press release](https://www.solo.io/press-releases/solo-io-launches-agent-gateway-and-introduces-agent-mesh)
- Cognitive Fabric Nodes (Istio-analog for agents): [arXiv:2604.03430](https://arxiv.org/html/2604.03430)
- Mesh Memory Protocol: [arXiv:2604.19540](https://arxiv.org/html/2604.19540)
- Internet of Agents survey: [arXiv:2505.07176](https://arxiv.org/html/2505.07176v1)
- Multi-agent orchestration survey: [arXiv:2601.13671](https://arxiv.org/html/2601.13671v1)
- Pick and Spin (K8s multi-LLM orchestration): [arXiv:2512.22402](https://arxiv.org/abs/2512.22402)
- KubeIntellect (K8s-native agent framework): [arXiv:2509.02449](https://arxiv.org/html/2509.02449)
- OTel GenAI semantic conventions: [opentelemetry.io](https://opentelemetry.io/blog/2025/ai-agent-observability/)
- Portkey resilience primitives: [portkey.ai](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/)
- Multi-agent LLM for incident response: [arXiv:2511.15755](https://arxiv.org/abs/2511.15755)
