# K8s Health Check Agent — AgentRoll Dogfooding Example

A simple AI agent that answers questions about Kubernetes cluster health. Built to dogfood AgentRoll — we use our own deployment framework to manage this agent.

## What It Does

Ask a question in natural language, get a diagnosis:

```bash
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"question": "Are there any unhealthy pods?"}'
```

The agent uses Claude's tool use to:
1. Query your Kubernetes cluster (pods, deployments, events, logs)
2. Analyze the results
3. Provide a clear diagnosis with suggested next steps

## Architecture

```
User Question
     │
     ▼
┌─────────────┐
│  FastAPI     │
│  /query      │
└──────┬──────┘
       │
       ▼
┌─────────────┐     ┌──────────────────┐
│  Claude API │────▶│  Tool Calls:     │
│  (tool use) │◀────│  - list_pods     │
└─────────────┘     │  - list_deploys  │
       │            │  - get_pod_logs  │
       ▼            │  - get_events    │
┌─────────────┐     └──────────────────┘
│  Diagnosis  │              │
│  Response   │              ▼
└─────────────┘     ┌──────────────────┐
                    │  Kubernetes API  │
                    └──────────────────┘
```

## Quick Start

### 1. Build the image

```bash
docker build -t k8s-health-agent:latest .

# Load into kind cluster
kind load docker-image k8s-health-agent:latest --name agentroll-dev
```

### 2. Set up credentials

```bash
# Create API key secret
kubectl create secret generic llm-credentials \
  --from-literal=anthropic-api-key=YOUR_ANTHROPIC_API_KEY

# Apply RBAC (agent needs read access to cluster resources)
kubectl apply -f k8s/rbac.yaml
```

### 3. Deploy with AgentRoll

```bash
# Make sure AgentRoll operator is running
cd /path/to/agentroll && make run

# In another terminal, deploy the agent
kubectl apply -f k8s/agent-deployment.yaml
```

### 4. Test it

```bash
# Port-forward to the agent
kubectl port-forward svc/k8s-health-agent 8080:8080

# Ask a question
curl -s -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{"question": "What deployments are running and are they healthy?"}' | jq .
```

## Dogfooding: Testing AgentRoll Features

### Prompt Version Change (Canary Deployment)

Edit `agent-deployment.yaml` to change the prompt version:

```yaml
  container:
    env:
      - name: PROMPT_VERSION
        value: "v2"    # Changed from v1
  agentMeta:
    promptVersion: "v2"  # Changed from v1
```

```bash
kubectl apply -f k8s/agent-deployment.yaml
```

AgentRoll will create a canary Rollout — 50% traffic to v2 for 2 minutes,
then promote to 100% if analysis passes.

Watch the rollout:
```bash
kubectl get rollouts -w
kubectl get agentdeployments
```

### Model Version Change

Switch from Claude Sonnet to a different model:

```yaml
  container:
    env:
      - name: LLM_MODEL
        value: "claude-haiku-4-20250514"  # Cheaper, faster, less capable
  agentMeta:
    modelVersion: "claude-haiku-4-20250514"
```

This triggers a new canary deployment — you can compare the quality
of responses between the stable (Sonnet) and canary (Haiku) versions.

## Local Development (without K8s)

```bash
pip install -r requirements.txt
export ANTHROPIC_API_KEY=your_key
uvicorn agent:app --reload --port 8080
```

Note: When running locally, the agent uses your kubeconfig to access
whatever cluster kubectl is configured for.
