"""
K8s Health Check Agent — A dogfooding example for AgentRoll.

This agent accepts natural language questions about Kubernetes cluster health
and uses Claude's tool use to query the cluster and provide diagnoses.

Example questions:
  - "What's the status of my deployments?"
  - "Are there any pods in CrashLoopBackOff?"
  - "How's the nginx deployment doing?"

This agent is intentionally simple — it demonstrates how to:
  1. Build a containerized agent with tool calling
  2. Deploy it with AgentRoll's AgentDeployment CRD
  3. Iterate on prompts and observe behavior changes via canary deployments
"""

import os
import json
import logging
import time
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from kubernetes import client, config
import anthropic

# Optional Langfuse instrumentation — only active when LANGFUSE_SECRET_KEY is set.
# Requires Langfuse Python SDK v2.x (langfuse>=2.0.0,<3.0.0).
# If the langfuse package is not installed, tracing is silently disabled.
try:
    from langfuse.decorators import observe, langfuse_context
    _LANGFUSE_AVAILABLE = True
except ImportError:
    _LANGFUSE_AVAILABLE = False
    # No-op decorator so @observe() on run_agent() is safe without the package
    def observe(name=None, **kwargs):
        def decorator(f):
            return f
        return decorator
    langfuse_context = None

LANGFUSE_ENABLED = _LANGFUSE_AVAILABLE and bool(os.getenv("LANGFUSE_SECRET_KEY"))

# Optional OpenTelemetry metrics — active when OTEL_EXPORTER_OTLP_ENDPOINT is set.
# The AgentRoll OTel sidecar (enabled via observability.opentelemetry.enabled=true) receives
# these metrics via OTLP and exposes them as Prometheus on port 8889, wired to Grafana.
try:
    from opentelemetry import metrics as otel_metrics
    from opentelemetry.sdk.metrics import MeterProvider
    from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
    from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
    from opentelemetry.sdk.resources import Resource
    _OTEL_AVAILABLE = True
except ImportError:
    _OTEL_AVAILABLE = False

OTEL_ENABLED = _OTEL_AVAILABLE and bool(os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

# ============================================================
# Configuration
# ============================================================

SYSTEM_PROMPT = """You are a Kubernetes cluster health assistant. Your job is to help
platform engineers understand the health of their Kubernetes workloads.

When asked about cluster health, use the available tools to query real cluster data,
then provide a clear, concise diagnosis. Focus on:
- What's healthy and what isn't
- Root causes of any issues
- Suggested next steps

Be direct and actionable. Don't repeat raw data — interpret it."""

# Degraded prompt for testing quality gates. Used when PROMPT_VERSION starts with
# "degraded-". This intentionally produces responses that fail the analysis runner:
#   - Under 30 words → fails response_length check (< 50 chars)
#   - No tool calls → fails tool_usage check
DEGRADED_SYSTEM_PROMPT = """You are a brief assistant. Do NOT use any tools.
Answer all questions in under 30 words using only your general knowledge.
Keep answers vague and non-specific."""

# This is v1 of the prompt. We'll iterate on it to test AgentRoll's
# canary deployment with prompt version changes.
PROMPT_VERSION = os.getenv("PROMPT_VERSION", "v1")
MODEL_VERSION = os.getenv("LLM_MODEL", "claude-sonnet-4-20250514")

# Select active prompt and whether tools are available based on version
_IS_DEGRADED = PROMPT_VERSION.startswith("degraded-")
ACTIVE_SYSTEM_PROMPT = DEGRADED_SYSTEM_PROMPT if _IS_DEGRADED else SYSTEM_PROMPT

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("k8s-health-agent")

# ============================================================
# OTel metrics setup
# ============================================================

if OTEL_ENABLED:
    _otel_resource = Resource.create({
        "service.name": "k8s-health-agent",
        "agentroll.prompt.version": PROMPT_VERSION,
        "agentroll.model.version": MODEL_VERSION,
    })
    _otel_reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(),
        export_interval_millis=10_000,
    )
    _otel_provider = MeterProvider(resource=_otel_resource, metric_readers=[_otel_reader])
    otel_metrics.set_meter_provider(_otel_provider)
    _meter = otel_metrics.get_meter("k8s-health-agent")
    _request_counter = _meter.create_counter(
        "agent.requests.total", description="Total agent requests processed"
    )
    _duration_histogram = _meter.create_histogram(
        "agent.request.duration", unit="s", description="Agent request duration in seconds"
    )
    _token_counter = _meter.create_counter(
        "agent.tokens.total", description="Total tokens consumed by agent LLM calls"
    )
    _tool_call_counter = _meter.create_counter(
        "agent.tool.calls.total", description="Total tool calls made by agent"
    )
    logger.info("OTel metrics enabled, exporting to %s", os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
else:
    class _Noop:
        def add(self, *a, **kw): pass
        def record(self, *a, **kw): pass
    _request_counter = _token_counter = _tool_call_counter = _Noop()
    _duration_histogram = _Noop()

# ============================================================
# Kubernetes client setup
# ============================================================

def get_k8s_client():
    """Initialize K8s client — works both in-cluster and local."""
    try:
        config.load_incluster_config()
        logger.info("Using in-cluster K8s config")
    except config.ConfigException:
        config.load_kube_config()
        logger.info("Using local kubeconfig")
    return client.CoreV1Api(), client.AppsV1Api()


core_v1, apps_v1 = get_k8s_client()

# ============================================================
# Tools — these are the actions the agent can take
# ============================================================

def list_pods(namespace: str = "default") -> str:
    """List all pods in a namespace with their status."""
    try:
        pods = core_v1.list_namespaced_pod(namespace)
        results = []
        for pod in pods.items:
            container_statuses = []
            if pod.status.container_statuses:
                for cs in pod.status.container_statuses:
                    status_detail = "Running"
                    if cs.state.waiting:
                        status_detail = f"Waiting: {cs.state.waiting.reason}"
                    elif cs.state.terminated:
                        status_detail = f"Terminated: {cs.state.terminated.reason}"
                    container_statuses.append({
                        "name": cs.name,
                        "ready": cs.ready,
                        "restarts": cs.restart_count,
                        "status": status_detail,
                    })
            results.append({
                "name": pod.metadata.name,
                "phase": pod.status.phase,
                "containers": container_statuses,
            })
        return json.dumps(results, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def list_deployments(namespace: str = "default") -> str:
    """List all deployments in a namespace with replica status."""
    try:
        deps = apps_v1.list_namespaced_deployment(namespace)
        results = []
        for dep in deps.items:
            results.append({
                "name": dep.metadata.name,
                "replicas": dep.spec.replicas,
                "ready": dep.status.ready_replicas or 0,
                "available": dep.status.available_replicas or 0,
                "unavailable": dep.status.unavailable_replicas or 0,
                "image": dep.spec.template.spec.containers[0].image if dep.spec.template.spec.containers else "unknown",
            })
        return json.dumps(results, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


def get_pod_logs(pod_name: str, namespace: str = "default", tail_lines: int = 20) -> str:
    """Get recent logs from a specific pod."""
    try:
        logs = core_v1.read_namespaced_pod_log(
            pod_name, namespace, tail_lines=tail_lines
        )
        return logs if logs else "(no logs)"
    except Exception as e:
        return json.dumps({"error": str(e)})


def get_events(namespace: str = "default") -> str:
    """Get recent cluster events, especially warnings."""
    try:
        events = core_v1.list_namespaced_event(namespace)
        results = []
        for event in sorted(events.items, key=lambda e: e.last_timestamp or e.event_time or "", reverse=True)[:15]:
            results.append({
                "type": event.type,
                "reason": event.reason,
                "message": event.message,
                "object": f"{event.involved_object.kind}/{event.involved_object.name}",
                "count": event.count,
            })
        return json.dumps(results, indent=2)
    except Exception as e:
        return json.dumps({"error": str(e)})


# Tool registry — maps tool names to functions
TOOLS = {
    "list_pods": list_pods,
    "list_deployments": list_deployments,
    "get_pod_logs": get_pod_logs,
    "get_events": get_events,
}

# Tool definitions for Claude API
TOOL_DEFINITIONS = [
    {
        "name": "list_pods",
        "description": "List all pods in a Kubernetes namespace with their status, container states, and restart counts.",
        "input_schema": {
            "type": "object",
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "Kubernetes namespace to query. Defaults to 'default'.",
                    "default": "default",
                }
            },
        },
    },
    {
        "name": "list_deployments",
        "description": "List all deployments in a Kubernetes namespace with replica counts and health status.",
        "input_schema": {
            "type": "object",
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "Kubernetes namespace to query. Defaults to 'default'.",
                    "default": "default",
                }
            },
        },
    },
    {
        "name": "get_pod_logs",
        "description": "Get recent log lines from a specific pod. Useful for diagnosing errors.",
        "input_schema": {
            "type": "object",
            "properties": {
                "pod_name": {
                    "type": "string",
                    "description": "Name of the pod to get logs from.",
                },
                "namespace": {
                    "type": "string",
                    "description": "Kubernetes namespace. Defaults to 'default'.",
                    "default": "default",
                },
                "tail_lines": {
                    "type": "integer",
                    "description": "Number of recent log lines to retrieve. Defaults to 20.",
                    "default": 20,
                },
            },
            "required": ["pod_name"],
        },
    },
    {
        "name": "get_events",
        "description": "Get recent Kubernetes events in a namespace, especially warnings that indicate problems.",
        "input_schema": {
            "type": "object",
            "properties": {
                "namespace": {
                    "type": "string",
                    "description": "Kubernetes namespace to query. Defaults to 'default'.",
                    "default": "default",
                }
            },
        },
    },
]

# ============================================================
# Agent loop — Claude tool use with multi-turn
# ============================================================

@observe(name="run_agent")
def run_agent(question: str) -> tuple[str, int]:
    """Run the agent: send question to Claude, execute tool calls, return (answer, tool_calls_count)."""
    api_key = os.getenv("ANTHROPIC_API_KEY")
    if not api_key:
        raise ValueError("ANTHROPIC_API_KEY environment variable not set")

    # Tag the Langfuse trace with version metadata so langfuse_metrics.py can filter it
    if LANGFUSE_ENABLED and langfuse_context is not None:
        composite_version = f"{PROMPT_VERSION}.{MODEL_VERSION}"
        langfuse_context.update_current_trace(
            metadata={
                "prompt_version": PROMPT_VERSION,
                "model_version": MODEL_VERSION,
                "composite_version": composite_version,
            },
            tags=[
                f"canary:{composite_version}",
                f"prompt:{PROMPT_VERSION}",
                f"model:{MODEL_VERSION}",
            ],
        )

    claude = anthropic.Anthropic(api_key=api_key)
    messages = [{"role": "user", "content": question}]

    # Degraded versions skip tools entirely — this is what the quality gate detects
    active_tools = [] if _IS_DEGRADED else TOOL_DEFINITIONS

    logger.info(f"Agent query: {question} | prompt={PROMPT_VERSION} model={MODEL_VERSION} tools={'disabled' if _IS_DEGRADED else 'enabled'}")

    tool_calls_count = 0

    # OTel: track request start time and token usage across all LLM calls
    _req_start = time.time()
    _input_tokens = 0
    _output_tokens = 0
    _otel_attrs = {"agent": "k8s-health-agent", "prompt_version": PROMPT_VERSION, "model_version": MODEL_VERSION}

    # Agent loop: Claude may request multiple tool calls before giving a final answer
    max_iterations = 5
    for i in range(max_iterations):
        create_kwargs = dict(
            model=MODEL_VERSION,
            max_tokens=2048,
            system=ACTIVE_SYSTEM_PROMPT,
            messages=messages,
        )
        if active_tools:
            create_kwargs["tools"] = active_tools

        response = claude.messages.create(**create_kwargs)

        # Accumulate token usage for OTel metrics
        if response.usage:
            _input_tokens += response.usage.input_tokens
            _output_tokens += response.usage.output_tokens

        # Check if Claude wants to call tools
        if response.stop_reason == "tool_use":
            # Process each tool call in the response
            tool_results = []
            for block in response.content:
                if block.type == "tool_use":
                    tool_name = block.name
                    tool_input = block.input
                    tool_calls_count += 1
                    logger.info(f"Tool call: {tool_name}({tool_input})")

                    result = _execute_tool(tool_name, tool_input)
                    tool_results.append({
                        "type": "tool_result",
                        "tool_use_id": block.id,
                        "content": result,
                    })

            # Add assistant response and tool results to conversation
            messages.append({"role": "assistant", "content": response.content})
            messages.append({"role": "user", "content": tool_results})
        else:
            # Claude gave a final text response
            final_text = ""
            for block in response.content:
                if hasattr(block, "text"):
                    final_text += block.text
            logger.info(f"Agent response complete | iterations={i+1} tool_calls={tool_calls_count}")
            # Record OTel metrics for this request
            _request_counter.add(1, attributes=_otel_attrs)
            _duration_histogram.record(time.time() - _req_start, attributes=_otel_attrs)
            _token_counter.add(_input_tokens, attributes={**_otel_attrs, "token_type": "input"})
            _token_counter.add(_output_tokens, attributes={**_otel_attrs, "token_type": "output"})
            return final_text, tool_calls_count

    # Record OTel metrics even on max-iterations path
    _request_counter.add(1, attributes=_otel_attrs)
    _duration_histogram.record(time.time() - _req_start, attributes=_otel_attrs)
    _token_counter.add(_input_tokens, attributes={**_otel_attrs, "token_type": "input"})
    _token_counter.add(_output_tokens, attributes={**_otel_attrs, "token_type": "output"})
    return "Agent reached maximum iterations without a final answer.", tool_calls_count


@observe(name="tool_call")
def _execute_tool(tool_name: str, tool_input: dict) -> str:
    """Execute a single tool call and return the result as a string. Traced as a Langfuse span."""
    if LANGFUSE_ENABLED and langfuse_context is not None:
        langfuse_context.update_current_observation(metadata={"tool": tool_name, "input": tool_input})
    _attrs = {"agent": "k8s-health-agent", "tool": tool_name}
    if tool_name in TOOLS:
        result = TOOLS[tool_name](**tool_input)
        try:
            _status = "error" if "error" in json.loads(result) else "success"
        except Exception:
            _status = "success"
        _tool_call_counter.add(1, attributes={**_attrs, "status": _status})
        return result
    _tool_call_counter.add(1, attributes={**_attrs, "status": "error"})
    return json.dumps({"error": f"Unknown tool: {tool_name}"})


# ============================================================
# FastAPI app
# ============================================================

app = FastAPI(
    title="K8s Health Check Agent",
    description="AI-powered Kubernetes cluster health diagnostics",
    version=PROMPT_VERSION,
)


class QueryRequest(BaseModel):
    question: str


class QueryResponse(BaseModel):
    answer: str
    prompt_version: str
    model_version: str
    tool_calls_count: int = 0


@app.get("/healthz")
def healthz():
    """Health check endpoint for Kubernetes probes."""
    return {"status": "ok", "prompt_version": PROMPT_VERSION, "model_version": MODEL_VERSION}


@app.post("/query", response_model=QueryResponse)
def query(req: QueryRequest):
    """Ask the agent a question about cluster health."""
    try:
        answer, tool_calls_count = run_agent(req.question)
        return QueryResponse(
            answer=answer,
            prompt_version=PROMPT_VERSION,
            model_version=MODEL_VERSION,
            tool_calls_count=tool_calls_count,
        )
    except Exception as e:
        logger.error(f"Agent error: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/")
def root():
    return {
        "name": "k8s-health-agent",
        "prompt_version": PROMPT_VERSION,
        "model_version": MODEL_VERSION,
        "endpoints": ["/query", "/healthz"],
    }
