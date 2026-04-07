# Langfuse Setup Guide for AgentRoll

This guide explains how to integrate Langfuse trace-based quality gates with AgentRoll. Langfuse collects traces from your agent during a canary deployment; the `agent-langfuse-check` AnalysisTemplate queries those traces to pass or fail the rollout.

Two setup paths are covered:

- **Option A: Self-hosted (local Kind)** — for development and local e2e testing
- **Option B: cloud.langfuse.com** — for staging and production

---

## Prerequisites

- A running Kind (or other Kubernetes) cluster with AgentRoll installed
- Argo Rollouts controller running in the cluster
- `kubectl` configured to point at the target cluster
- Docker (for Option A only)

---

## SDK Version Note

The agent must use the **Langfuse Python SDK v2**, not v3 or v4. Langfuse v4 switched to OpenTelemetry transport, which is incompatible with the Langfuse v2 server API that `langfuse_metrics.py` queries.

```
langfuse>=2.0.0,<3.0.0
```

Pin this in your `requirements.txt`. The `k8s-health-agent` example already does this.

---

## Option A: Self-Hosted (Local Kind)

Use this path when developing locally against a Kind cluster. Langfuse v2 runs as two Docker containers (Postgres + the app) on your host machine. Kind pods reach it over the Docker bridge network.

### Step 1 — Start Langfuse

```bash
cd docs/langfuse
docker compose up -d
```

Wait about 15 seconds for the database migration and headless init to complete, then verify:

```bash
curl -s http://localhost:3000/api/public/health
# Expected: {"status":"ok"}
```

The `docker-compose.yml` pre-seeds a project and admin account on first boot:

| Field        | Value                        |
|--------------|------------------------------|
| UI URL       | http://localhost:3000        |
| Login email  | admin@agentroll.dev          |
| Password     | agentroll-dev                |
| Public key   | pk-agentroll-local           |
| Secret key   | sk-agentroll-local           |

### Step 2 — Create the Kubernetes Secret

Kind pods cannot reach `localhost` on the host — they use the Docker bridge gateway IP instead (`192.168.97.1`). The secret holds the same pre-seeded keys:

```bash
kubectl create secret generic langfuse-credentials \
  --from-literal=public-key=pk-agentroll-local \
  --from-literal=secret-key=sk-agentroll-local
```

### Step 3 — Deploy the Agent with Langfuse Vars

Apply the provided example manifest, which already sets `LANGFUSE_HOST` to the Kind bridge gateway:

```bash
kubectl apply -f examples/k8s-health-agent/k8s/rbac.yaml
kubectl apply -f examples/k8s-health-agent/k8s/agent-deployment-langfuse.yaml
```

The relevant env vars in that manifest are:

```yaml
- name: LANGFUSE_HOST
  value: "http://192.168.97.1:3000"
- name: LANGFUSE_PUBLIC_KEY
  valueFrom:
    secretKeyRef:
      name: langfuse-credentials
      key: public-key
- name: LANGFUSE_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: langfuse-credentials
      key: secret-key
```

For your own agent, add the same three env vars with the same secret reference.

### Step 4 — Apply the AnalysisTemplate

The `agent-langfuse-check` template defaults to `https://cloud.langfuse.com`. For local Kind, patch the default to the bridge gateway:

```bash
# Option 1: apply as-is and override langfuse-host at the AgentDeployment level (see Step 5)

# Option 2: edit the template before applying to change the default
sed 's|https://cloud.langfuse.com|http://192.168.97.1:3000|' \
  templates/analysis/agent-langfuse-check.yaml | kubectl apply -f -
```

Or apply unchanged and pass the host as an arg in your AgentDeployment (Option 1 above is usually cleaner for local dev).

```bash
kubectl apply -f templates/analysis/agent-langfuse-check.yaml
```

Verify the template is registered:

```bash
kubectl get analysistemplate agent-langfuse-check
```

### Step 5 — Reference the Template in Your AgentDeployment

Add `templateRef: agent-langfuse-check` to your rollout step. The AgentRoll controller automatically injects the `canary-version` argument from the composite version (`{promptVersion}.{modelVersion}`).

```yaml
rollout:
  strategy: canary
  steps:
    - setWeight: 20
      pause:
        duration: "2m"
      analysis:
        templateRef: agent-langfuse-check
        args:
          - name: langfuse-host
            value: "http://192.168.97.1:3000"
    - setWeight: 100
```

If you pre-patched the template default in Step 4, you can omit the `args` block.

---

## Option B: cloud.langfuse.com (Production)

Use this path for staging and production deployments.

### Step 1 — Create a Langfuse Cloud Account and Project

1. Sign up at https://cloud.langfuse.com (free tier available).
2. Create a new project (e.g., `k8s-health-agent-prod`).
3. Go to **Settings > API Keys** and generate a key pair. Note the public key (`pk-lf-...`) and secret key (`sk-lf-...`).

### Step 2 — Create the Kubernetes Secret

```bash
kubectl create secret generic langfuse-credentials \
  --from-literal=public-key=pk-lf-YOUR_PUBLIC_KEY \
  --from-literal=secret-key=sk-lf-YOUR_SECRET_KEY
```

Replace the placeholder values with the keys from Step 1.

### Step 3 — Deploy the Agent with Langfuse Vars

Add the following env vars to your agent's container spec. `LANGFUSE_HOST` can be omitted since `https://cloud.langfuse.com` is the SDK default, but it is recommended to set it explicitly:

```yaml
- name: LANGFUSE_HOST
  value: "https://cloud.langfuse.com"
- name: LANGFUSE_PUBLIC_KEY
  valueFrom:
    secretKeyRef:
      name: langfuse-credentials
      key: public-key
- name: LANGFUSE_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: langfuse-credentials
      key: secret-key
```

Langfuse instrumentation in `agent.py` is gated on `LANGFUSE_SECRET_KEY` being set — if the var is absent, tracing is silently disabled and the agent runs normally.

### Step 4 — Apply the AnalysisTemplate

```bash
kubectl apply -f templates/analysis/agent-langfuse-check.yaml
```

The template's `langfuse-host` argument already defaults to `https://cloud.langfuse.com`, so no patching is needed.

### Step 5 — Reference the Template in Your AgentDeployment

```yaml
rollout:
  strategy: canary
  steps:
    - setWeight: 20
      pause:
        duration: "2m"
      analysis:
        templateRef: agent-langfuse-check
    - setWeight: 100
```

---

## How Agent Instrumentation Works

The `examples/k8s-health-agent/agent.py` shows the required pattern. Two key pieces:

**1. Decorator on the main agent function:**

```python
from langfuse.decorators import observe, langfuse_context

@observe(name="run_agent")
def run_agent(question: str) -> tuple[str, int]:
    ...
```

**2. Trace tagging with the composite version:**

```python
if LANGFUSE_ENABLED and langfuse_context is not None:
    composite_version = f"{PROMPT_VERSION}.{MODEL_VERSION}"
    langfuse_context.update_current_trace(
        tags=[f"canary:{composite_version}"],
        metadata={
            "prompt_version": PROMPT_VERSION,
            "model_version": MODEL_VERSION,
        },
    )
```

The `canary:<composite-version>` tag is what `langfuse_metrics.py` uses to filter traces belonging to the canary. The composite version format is `{promptVersion}.{modelVersion}` — matching the value the AgentRoll controller injects as the `canary-version` AnalysisTemplate argument.

**3. Tool call spans:**

Each tool call must be wrapped with `@observe(name="tool_call")`. The metrics script identifies tool call spans by type `SPAN` and name prefix `tool_call`. `_execute_tool` in `agent.py` does this:

```python
@observe(name="tool_call")
def _execute_tool(tool_name: str, tool_input: dict) -> str:
    ...
```

---

## What the Quality Gate Checks

`langfuse_metrics.py` runs as a Kubernetes Job during the AnalysisRun. It:

1. Queries `GET /api/public/traces?tags=canary:<version>&fromTimestamp=<now-10m>` to find traces from the canary.
2. For each trace, fetches observations from `GET /api/public/observations?traceId=<id>`.
3. Counts `SPAN` observations named `tool_call*` as tool call attempts; counts those without level `ERROR` as successes.
4. Exits 0 (pass) if `success_rate >= MIN_SUCCESS_RATE` (default 0.90).
5. Exits 0 with `"inconclusive": true` if fewer than `MIN_TRACES` (default 5) traces exist — this avoids false negatives during canary warmup.
6. Exits 1 (fail) if the success rate is below the threshold.

The AnalysisTemplate runs this check every 2 minutes, up to 3 times, with a failure limit of 1.

---

## Tuning Parameters

Override these args in your AgentDeployment `analysis` block:

| Arg                   | Default | Description                                                          |
|-----------------------|---------|----------------------------------------------------------------------|
| `langfuse-host`       | `https://cloud.langfuse.com` | Langfuse server URL                          |
| `langfuse-secret-name`| `langfuse-credentials`       | Name of the K8s secret holding keys         |
| `min-success-rate`    | `0.90`  | Minimum tool call success rate (0.0–1.0). Lower for initial rollout. |
| `time-window-minutes` | `10`    | How many minutes back to look for traces. Increase for low-traffic agents. |
| `min-traces`          | `5`     | Minimum traces required before making a pass/fail judgment.          |

Example override:

```yaml
analysis:
  templateRef: agent-langfuse-check
  args:
    - name: min-success-rate
      value: "0.80"
    - name: time-window-minutes
      value: "20"
    - name: min-traces
      value: "3"
```

---

## Verification

### 1. Send test queries to the agent

```bash
AGENT_POD=$(kubectl get pod -l app=k8s-health-agent -o jsonpath='{.items[0].metadata.name}')
kubectl exec -it "$AGENT_POD" -- curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"question": "Are there any pods in CrashLoopBackOff?"}'
```

Or port-forward and query from your machine:

```bash
kubectl port-forward svc/k8s-health-agent 8080:8080 &
curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"question": "What is the status of my deployments?"}'
```

### 2. Confirm traces appear in Langfuse

For local Kind:

```bash
curl -s -u "pk-agentroll-local:sk-agentroll-local" \
  "http://localhost:3000/api/public/traces" | python3 -m json.tool | head -40
```

For Langfuse Cloud (replace keys):

```bash
curl -s -u "pk-lf-...:sk-lf-..." \
  "https://cloud.langfuse.com/api/public/traces" | python3 -m json.tool | head -40
```

Expect to see trace objects with `tags` containing `canary:<composite-version>`.

### 3. Run langfuse_metrics.py locally

This lets you validate the quality gate logic before triggering a canary:

```bash
# For local Kind
export LANGFUSE_HOST=http://localhost:3000
export LANGFUSE_PUBLIC_KEY=pk-agentroll-local
export LANGFUSE_SECRET_KEY=sk-agentroll-local
export CANARY_VERSION="v4.claude-haiku-4-5-20251001"
export METRIC=tool_success_rate
export MIN_SUCCESS_RATE=0.90
export TIME_WINDOW_MINUTES=60
export MIN_TRACES=1

python3 templates/analysis/langfuse_metrics.py
```

Exit code 0 = pass, exit code 1 = fail. The script prints a JSON result and human-readable log lines.

### 4. Trigger a canary and watch the AnalysisRun

Apply a new AgentDeployment revision (e.g., bump `promptVersion`):

```bash
kubectl apply -f examples/k8s-health-agent/k8s/agent-deployment-langfuse.yaml
```

Watch the AnalysisRun:

```bash
kubectl get analysisrun -w
kubectl describe analysisrun <run-name>
```

The `tool-call-success-rate` metric will show `Successful`, `Failed`, or `Inconclusive` after each 2-minute interval. If the run fails, Argo Rollouts rolls back automatically (when `rollback.onFailedAnalysis: true` is set).

---

## Troubleshooting

**Traces not appearing in Langfuse:**
- Confirm `LANGFUSE_SECRET_KEY` is set in the pod — the agent silently disables tracing if it is absent.
- Check pod logs for `langfuse` SDK errors: `kubectl logs <pod> | grep -i langfuse`.
- For Kind, verify the pod can reach the host: `kubectl exec <pod> -- curl -s http://192.168.97.1:3000/api/public/health`.

**AnalysisRun shows `Inconclusive` repeatedly:**
- Lower `min-traces` if the canary receives little traffic.
- Increase `time-window-minutes` so the window captures more traces.

**AnalysisRun fails immediately with `missing_config`:**
- The Job cannot read the Langfuse secret. Verify: `kubectl get secret langfuse-credentials`.
- Check that the secret key names are exactly `public-key` and `secret-key`.

**Tool calls not detected (success rate 0% with no tool spans):**
- Confirm tool execution functions are decorated with `@observe(name="tool_call")`.
- The span name must start with `tool_call`. Names like `execute_tool` will not be counted.

**`ImportError: No module named 'langfuse'`:**
- The Langfuse SDK is not installed in the container image. Add `langfuse>=2.0.0,<3.0.0` to `requirements.txt` and rebuild.
- Do not use `langfuse>=3.0.0` — v3/v4 uses OpenTelemetry transport which is incompatible with the v2 server REST API.
