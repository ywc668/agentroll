# AgentDeployment API Reference

**Group:** `agentroll.dev`  
**Version:** `v1alpha1`  
**Kind:** `AgentDeployment`

> This document is generated from the Go type definitions in `api/v1alpha1/agentdeployment_types.go`.  
> The authoritative schema is in `config/crd/bases/agentroll.dev_agentdeployments.yaml`.

---

## AgentDeployment

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: my-agent
  namespace: default
spec: ...
status: ...
```

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `container` | [AgentContainerSpec](#agentcontainerspec) | Yes | Agent container configuration |
| `agentMeta` | [AgentMetaSpec](#agentmetaspec) | No | Version identity: prompt, model, tools |
| `rollout` | [RolloutSpec](#rolloutspec) | Yes | Progressive delivery strategy |
| `rollback` | [RollbackSpec](#rollbackspec) | No | Automatic rollback conditions |
| `observability` | [ObservabilitySpec](#observabilityspec) | No | OTel, Langfuse monitoring |
| `scaling` | [ScalingSpec](#scalingspec) | No | Queue-depth autoscaling (KEDA) |
| `serviceAccountName` | string | No | ServiceAccount for agent pods; auto-created if empty |
| `replicas` | int32 | No | Desired replica count (default: 1; ignored when `scaling` is set) |
| `dependsOn` | []string | No | A2A dependencies: other AgentDeployments that must be Stable before this agent's canary progresses |

---

### AgentContainerSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | string | Yes | Container image (e.g., `ghcr.io/my-org/agent:v1.2.3`) |
| `env` | []EnvVar | No | Environment variables (supports `valueFrom: secretKeyRef`) |
| `resources` | ResourceRequirements | No | CPU/memory requests and limits |
| `ports` | []ContainerPort | No | Exposed ports; triggers Service creation |
| `command` | []string | No | Entrypoint override |
| `args` | []string | No | Arguments to the entrypoint |

---

### AgentMetaSpec

Captures the four layers of agent identity. Changing any layer changes agent behavior.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `promptVersion` | string | No | Prompt/system context version (git ref, semver, or tag) |
| `modelVersion` | string | No | LLM model identifier (e.g., `claude-sonnet-4-20250514`) |
| `modelProvider` | string | No | LLM provider (`anthropic`, `openai`, `local`) |
| `toolDependencies` | [][ToolDependency](#tooldependency) | No | MCP tool servers with version constraints |

#### ToolDependency

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Tool name (also used to discover a matching Kubernetes Service) |
| `version` | string | Semver constraint (e.g., `>=1.2.0`) — blocks rollout if not met |
| `endpoint` | string | Override endpoint; skips Service discovery if set |

---

### RolloutSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `strategy` | string | Yes | Rollout strategy. Currently supports `"canary"` |
| `steps` | [][RolloutStep](#rolloutstep) | Yes | Ordered list of canary steps |

#### RolloutStep

Each step is either a weight change or a pause:

| Field | Type | Description |
|-------|------|-------------|
| `setWeight` | int32 | Route this percentage of traffic to the canary |
| `pause` | *PauseStep | Pause execution; set `duration: "0"` for a manual gate |
| `analysis` | *StepAnalysis | Run an AnalysisTemplate at this step |

#### StepAnalysis

| Field | Type | Description |
|-------|------|-------------|
| `templateRef` | string | Name of the `AnalysisTemplate` to run |
| `args` | map[string]string | Arguments to pass to the template |

> **Managed templates:** `agent-quality-check` and `agent-cost-check` are auto-created by AgentRoll. Any other name is treated as user-managed and left untouched.

---

### RollbackSpec

| Field | Type | Description |
|-------|------|-------------|
| `onFailedAnalysis` | bool | Roll back when canary analysis fails (default: true) |
| `onCostSpike` | *CostSpikeSpec | Roll back when canary token cost exceeds a threshold |

#### CostSpikeSpec

| Field | Type | Description |
|-------|------|-------------|
| `threshold` | string | Maximum acceptable cost ratio (e.g., `"200%"` = canary costs at most 2x stable) |

When `onCostSpike` is configured, AgentRoll injects an `agent-cost-check` analysis step that queries Langfuse for token cost data.

---

### ObservabilitySpec

| Field | Type | Description |
|-------|------|-------------|
| `opentelemetry` | *OTelSpec | OTel sidecar injection |
| `langfuse` | *LangfuseSpec | Langfuse trace integration |

#### OTelSpec

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Inject an OTel Collector sidecar and set `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `collectorEndpoint` | string | Override the default collector endpoint (`otel-collector.monitoring:4317`) |

When enabled, a ConfigMap named `<agent-name>-otel-config` is created with the OTel Collector configuration.

#### LangfuseSpec

| Field | Type | Description |
|-------|------|-------------|
| `endpoint` | string | Langfuse server URL (default: `https://cloud.langfuse.com`) |
| `projectID` | string | Langfuse project ID |
| `secretRef` | string | Kubernetes Secret containing `LANGFUSE_PUBLIC_KEY` + `LANGFUSE_SECRET_KEY` (default: `langfuse-credentials`) |

---

### ScalingSpec

Queue-depth based autoscaling via KEDA. Requires KEDA to be installed in the cluster.

| Field | Type | Description |
|-------|------|-------------|
| `minReplicas` | int32 | Minimum number of agent pods |
| `maxReplicas` | int32 | Maximum number of agent pods |
| `metric` | string | Scaling trigger metric (e.g., `queue-depth`) |
| `targetValue` | int32 | Target metric value per pod (scale up when queue > target × current replicas) |
| `queueRef` | *QueueReference | Queue to monitor |

#### QueueReference

| Field | Type | Description |
|-------|------|-------------|
| `provider` | string | Queue provider: `redis`, `rabbitmq`, or `sqs` |
| `address` | string | Queue broker address or SQS URL |
| `queueName` | string | Name of the queue to monitor |

---

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | Current phase: `Pending`, `Progressing`, `Stable`, `Degraded`, `RollingBack` |
| `compositeVersion` | string | Current composite version: `{promptVersion}.{modelVersion}.{imageTag}` |
| `stableVersion` | string | Composite version of the last known-good (stable) deployment |
| `canaryWeight` | int32 | Current canary traffic weight (0–100) |
| `conditions` | []Condition | Standard Kubernetes conditions: `Available`, `Progressing`, `Degraded` |
| `observedGeneration` | int64 | Last reconciled `.metadata.generation` |

### Phase Values

| Phase | Meaning |
|-------|---------|
| `Pending` | Initial state; first Rollout being created |
| `Progressing` | Canary rollout in flight |
| `Stable` | All traffic on the current version; analysis passed |
| `Degraded` | Canary analysis failed; may require manual intervention |
| `RollingBack` | Canary rejected; rolling back to stable version |

---

## Examples

### Minimal deployment

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: my-agent
spec:
  container:
    image: ghcr.io/my-org/agent:v1.0.0
  rollout:
    strategy: canary
    steps:
      - setWeight: 20
      - setWeight: 100
```

### Full production deployment

```yaml
apiVersion: agentroll.dev/v1alpha1
kind: AgentDeployment
metadata:
  name: k8s-health-agent
spec:
  container:
    image: ghcr.io/my-org/k8s-health-agent:v2.1.0
    ports:
      - containerPort: 8080
        name: http
    env:
      - name: ANTHROPIC_API_KEY
        valueFrom:
          secretKeyRef:
            name: llm-secrets
            key: anthropic-api-key
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: 512Mi
  agentMeta:
    promptVersion: v2.1
    modelVersion: claude-sonnet-4-20250514
    modelProvider: anthropic
  rollout:
    strategy: canary
    steps:
      - setWeight: 20
        analysis:
          templateRef: agent-quality-check
      - setWeight: 100
  rollback:
    onFailedAnalysis: true
    onCostSpike:
      threshold: "200%"
  observability:
    opentelemetry:
      enabled: true
    langfuse:
      secretRef: langfuse-credentials
  scaling:
    minReplicas: 2
    maxReplicas: 20
    targetValue: 10
    queueRef:
      provider: redis
      address: redis.default.svc:6379
      queueName: agent-tasks
```
