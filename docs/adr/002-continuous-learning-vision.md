# ADR-002: Continuous Learning Loop — AgentRoll's Long-Term Vision

## Status
Draft — research phase

## Date
2026-03-26

## Context
Microsoft's Agent Lightning project demonstrates an offline training loop for agents:
Trainer (RL/APO optimization) + LightningStore (trajectory storage). It optimizes
agent behavior from production traces but lacks a safe deployment layer.

AgentRoll provides the deployment layer (canary, analysis, rollback) but currently
treats agents as static deployable units — no learning feedback loop.

## Vision: The Full Loop
```
Agent Online Service (AgentRoll manages deployment)
        │
        ▼
Trace Collection (OTel sidecar → trace store)
        │
        ▼
Offline Training (Agent Lightning / RL / APO)
        │
        ▼
New Model or Prompt (training output)
        │
        ▼
Progressive Delivery (AgentRoll canary → evaluate → promote)
        │
        ▼
Back to Online Service (loop continues)
```

## Phased Roadmap

- **v1** — Deployment (current): CRD, Argo Rollouts, AnalysisTemplate, canary
- **v2** — Observability: collect agent trajectories and reward signals
- **v3** — Training integration: connect to Agent Lightning or similar
- **v4** — Continuous learning: fully automated optimize → deploy → evaluate loop

## Why This Matters

This positions AgentRoll as the orchestration layer that glues together:
- Training (Agent Lightning, custom RL pipelines)
- Serving (vLLM, SGLang, API providers)
- Delivery (Argo Rollouts)
- Observability (OTel, Langfuse)

No existing tool does this end-to-end.

## References
- Microsoft Agent Lightning: https://github.com/microsoft/agent-lightning
- AgentRoll ADR-001: Build on Argo Rollouts
