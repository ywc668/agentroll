# AgentRoll: Single-Agent Lifecycle Roadmap

> **Goal**: Make AgentRoll the definitive Kubernetes-native platform for managing the complete
> lifecycle of a single AI agent — from first deploy through continuous self-improvement.

---

## Where We Stand vs. the Field

### What AgentRoll has that no one else does

| Capability | AgentRoll | LangSmith | Braintrust | Langfuse | Patronus |
|---|---|---|---|---|---|
| Composite versioning (`{prompt}.{model}.{image}`) | ✅ | ❌ | ❌ | ❌ | ❌ |
| Progressive delivery (canary + traffic split) | ✅ | ❌ | ❌ | ❌ | ❌ |
| Quality-gate auto-rollback | ✅ | ❌ | ❌ | ❌ | ❌ |
| Threshold write-back from prod data | ✅ (Sprint 8) | ❌ | ❌ | ❌ | ❌ |
| Prompt ConfigMap → live injection | ✅ (Sprint 8) | ❌ | ❌ | ❌ | ❌ |

### What the field has that we don't yet

- **LLM-as-Judge / Agent-as-Judge** continuous evaluation on prod traces (LangSmith, Braintrust, Patronus)
- **A/B testing prompts** against live traffic with statistical significance (Braintrust, LangSmith)
- **Automatic prompt optimization** from prod feedback — DSPy/Zenbase closes the loop that our prompt
  optimizer only approximates
- **Tool-level experimentation** — did adding/removing tool X change success rate? Nobody does this.
- **Agent memory** with measurable performance impact (Mem0 shows 26% quality gain, 91% latency reduction)
- **Memory drift detection** — no platform monitors whether accumulated memory is degrading over time

### The honest gap in Sprint 7+8

The evolution loop we built does _heuristic adaptation_, not learning:
- Threshold tuning is statistical (mean ± 1.5σ) — correct, but not data-driven optimization
- Prompt optimization suggests changes but has no way to know if the last change helped
- No feedback loop: did the new thresholds produce fewer false rollbacks? Unknown.

---

## Roadmap

### Sprint 9 — Evaluation Foundation (the missing layer)

**Theme**: Before we can improve an agent, we need to measure it correctly in production.

| Item | What it does | Why now |
|---|---|---|
| **9.1 LLM-as-Judge scorer** | Add an `agentroll.dev/judge` AnalysisTemplate that calls an LLM to score the canary's outputs against the stable version's outputs on the same inputs. Verdict: pass/fail + numeric quality score (0–1). | Currently our analysis is binary (exit code). We need a continuous quality signal to drive threshold tuning and prompt evolution. |
| **9.2 Eval ConfigMap** | `spec.evaluation.configMap` holds eval criteria (rubric, few-shot examples, scoring instructions). Versioned alongside the agent, injectable into judge Jobs. | Separates evaluation logic from analysis infrastructure. |
| **9.3 Quality score sink to Langfuse** | After each AnalysisRun, write judge scores as Langfuse scores (`/api/public/scores`) tagged with the composite version. | The threshold tuner already reads Langfuse scores. This closes the loop: judge scores → Langfuse → threshold tuner. |
| **9.4 Eval history in status** | `status.evalHistory`: last 50 quality scores with timestamps and composite version. Used by threshold tuner and plateau detector. | Ground truth for all adaptive logic. |

**Outcome**: For the first time, AgentRoll has a real quality number per canary, not just pass/fail.

---

### Sprint 10 — Prompt A/B Testing Loop

**Theme**: Test prompt variants scientifically, auto-promote the winner.

| Item | What it does |
|---|---|
| **10.1 PromptVariant CRD** | New `PromptVariant` resource: holds `systemPrompt` text, `parentVersion` (what it was derived from), `hypothesis` (why this should be better). |
| **10.2 Traffic-split evaluation** | When `spec.evolution.promptExperiment` references a `PromptVariant`, the controller creates a second canary at 10% traffic with the variant prompt, runs `LLM-as-Judge` on both, collects scores for N canaries. |
| **10.3 Auto-promotion** | If variant scores better than control with p < 0.05 (two-sample t-test over eval history), auto-promote: update `spec.evolution.promptConfigMap` with the winning prompt, bump `spec.agentMeta.promptVersion`. |
| **10.4 Prompt lineage in status** | `status.promptLineage`: chain of prompt versions with quality scores at each generation. Answers "did this prompt change help?" |

**Research grounding**: Braintrust and LangSmith do A/B testing but require human judgment. We auto-promote with statistical significance. Zenbase/DSPy do automatic optimization but outside deployment context. AgentRoll uniquely ties prompt experiment outcomes to traffic rollback safety.

---

### Sprint 11 — Tool Management

**Theme**: Treat agent tools as first-class versioned artifacts, not just code in Git.

| Item | What it does |
|---|---|
| **11.1 ToolDependency versioning** | `spec.agentMeta.toolDependencies` already exists. Extend: track tool version as part of composite version string (`{prompt}.{model}.{image}.{toolsHash}`). |
| **11.2 Tool-level analysis** | Add `agent-tool-check` managed AnalysisTemplate: calls the agent, counts tool invocations per type, measures per-tool success rate and latency. Records results as Langfuse scores by tool name. |
| **11.3 Tool experiment** | `spec.evolution.toolExperiment`: add/remove a tool from the canary's MCP server list and measure effect on quality scores and task success rate via the judge. |
| **11.4 Tool success rate threshold** | Threshold tuner picks up per-tool success rate scores from Langfuse, tunes `MIN_TOOL_SUCCESS_RATE` per tool. Analysis Job checks this per-tool. |

**Research grounding**: No platform does tool-level A/B testing. The Tool/Agent Selection Survey (preprints.org/202512.1050) identifies this as the biggest gap in production agent tooling. AgentRoll would be first.

---

### Sprint 12 — Agent Memory Lifecycle

**Theme**: Agents with memory need lifecycle management for their memory too.

| Item | What it does |
|---|---|
| **12.1 Memory backend spec** | `spec.memory`: reference to a Mem0 or MemGPT-compatible backend (service endpoint + secret). Controller injects `MEM0_API_URL`, `AGENT_SESSION_ID` env vars. |
| **12.2 Memory version snapshot** | Before canary promotion, snapshot the agent's current memory state (call `/api/memory/export`). Tagged with composite version. Stored as a Kubernetes Secret or PVC. |
| **12.3 Memory rollback** | On canary failure, rollback includes restoring the pre-canary memory snapshot, not just rolling back the pod image. |
| **12.4 Memory drift detection** | New periodic analysis: query last N quality scores over a 30-day window. If quality is trending down despite stable composite version, flag `MemoryDrift` condition. Trigger `memory-reset` strategy in evolution loop. |
| **12.5 Memory quality score** | Agent-as-judge eval specifically tests memory: asks the agent a question that requires recalling something from a past session. Score the accuracy. |

**Research grounding**: Mem0 shows 26% quality improvement and 91% latency reduction vs. OpenAI memory (arXiv:2504.19413). Memory drift and conflict resolution are identified open problems. AgentRoll would be the first deployment platform with memory lifecycle management.

---

### Sprint 13 — Continuous Optimization Loop (closing the full circle)

**Theme**: Connect every feedback signal into a self-reinforcing improvement loop.

```
Production traces
    ↓
Langfuse scores (judge + tool + memory)
    ↓
Threshold tuner (statistical baseline)  →  AnalysisTemplate env vars (immediate)
    ↓
Prompt optimizer (LLM-suggested change)  →  PromptVariant A/B test
    ↓
Outcome: winner promoted, loser archived
    ↓
Quality history (did this generation improve on the last?)
    ↓
Model upgrader (when quality plateaus after N cycles)
```

| Item | What it does |
|---|---|
| **13.1 Generation tracking** | Each evolution cycle is a "generation" with a unique ID. Status tracks: generation number, strategies run, quality delta vs. previous generation. |
| **13.2 Regression detection** | If current generation's average quality is worse than N-2 generations ago, rollback evolution: restore previous thresholds and previous prompt. Alert via Kubernetes Event. |
| **13.3 DSPy-style optimization integration** | Optional: `spec.evolution.optimizer.mode: dspy` — instead of single LLM suggestion, run DSPy's `MIPROv2` optimizer offline using the eval history as training signal. Apply result as a new `PromptVariant` for A/B test. |
| **13.4 Evolution scorecard** | `kubectl describe agentdeployment foo` shows: current generation, quality trend (↑↓→), strategies run this week, estimated quality delta from evolution. |

---

## Capability Map (End State)

```
┌─────────────────────────────────────────────────────────────────┐
│                    AgentDeployment CRD                          │
│                                                                 │
│  spec.container          → Pod image + env                      │
│  spec.agentMeta          → Composite version tracking           │
│  spec.rollout            → Canary steps + weights               │
│  spec.evaluation         → LLM-as-judge rubric + ConfigMap      │  ← Sprint 9
│  spec.evolution          → Threshold/prompt/model/tool/memory   │
│    .promptExperiment     → PromptVariant A/B test               │  ← Sprint 10
│    .toolExperiment       → Tool-level experiment                │  ← Sprint 11
│    .memory               → Memory backend + lifecycle           │  ← Sprint 12
│  spec.observability      → Langfuse + OTel                      │
│                                                                 │
│  status.evalHistory      → Quality scores per canary            │  ← Sprint 9
│  status.promptLineage    → Prompt version chain + quality       │  ← Sprint 10
│  status.evolution        → Thresholds, history, generation      │
│  status.memory           → Snapshot refs, drift condition       │  ← Sprint 12
└─────────────────────────────────────────────────────────────────┘
```

---

## Key Research References

- Agent-as-a-Judge: [arXiv:2410.10934](https://arxiv.org/abs/2410.10934)
- Measuring Agents in Production: [arXiv:2512.04123](https://arxiv.org/html/2512.04123v1)
- Beyond Accuracy (enterprise eval): [arXiv:2511.14136](https://arxiv.org/html/2511.14136v1)
- Mem0 production memory: [arXiv:2504.19413](https://arxiv.org/abs/2504.19413)
- Tool/Agent Selection Survey: [preprints.org/202512.1050](https://www.preprints.org/manuscript/202512.1050)
- DSPy prompt optimization: [arXiv:2507.03620](https://arxiv.org/html/2507.03620v1)
- Zenbase (DSPy-based continuous optimization): [YC launch](https://www.ycombinator.com/launches/Lmp-zenbase-continuous-prompt-optimization-from-dspy-core-contributors)
- AgentProp-Bench (tool-use eval): [arXiv:2604.16706](https://arxiv.org/html/2604.16706)
- TRAIL trace debugging: [arXiv:2505.08638](https://arxiv.org/html/2505.08638v1)
