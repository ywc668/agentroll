"""
K8s Health Agent — AutoGen dogfooding example for AgentRoll.

Uses AutoGen's AssistantAgent with registered tools and Anthropic API.
The agent runs an async tool-calling loop and is exposed via a synchronous
FastAPI endpoint using asyncio.

Example questions:
  - "What's the status of my deployments?"
  - "Are there any pods in CrashLoopBackOff?"
  - "How's the nginx deployment doing?"
"""

import os
import json
import logging
import asyncio

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

# AutoGen imports
from autogen_agentchat.agents import AssistantAgent
from autogen_agentchat.messages import TextMessage
from autogen_agentchat.ui import Console
from autogen_core import CancellationToken
from autogen_ext.models.anthropic import AnthropicChatCompletionClient

# Kubernetes — gracefully degrade if not available
try:
    from kubernetes import client, config as k8s_config

    def _init_k8s():
        try:
            k8s_config.load_incluster_config()
        except k8s_config.ConfigException:
            k8s_config.load_kube_config()
        return client.CoreV1Api(), client.AppsV1Api()

    _core_v1, _apps_v1 = _init_k8s()
    _K8S_AVAILABLE = True
except Exception:
    _K8S_AVAILABLE = False
    _core_v1 = _apps_v1 = None

# ============================================================
# Configuration
# ============================================================

PROMPT_VERSION = os.getenv("PROMPT_VERSION", "v1")
MODEL_VERSION = os.getenv("MODEL_VERSION", os.getenv("LLM_MODEL", "claude-haiku-4-5-20251001"))

SYSTEM_PROMPT = """You are a Kubernetes cluster health assistant. Your job is to help
platform engineers understand the health of their Kubernetes workloads.

When asked about cluster health, use the available tools to query real cluster data,
then provide a clear, concise diagnosis. Focus on:
- What's healthy and what isn't
- Root causes of any issues
- Suggested next steps

Be direct and actionable. Don't repeat raw data — interpret it."""

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("autogen-agent")

# ============================================================
# Tool functions registered with AutoGen
# ============================================================

async def list_pods(namespace: str = "default") -> str:
    """List all pods in a Kubernetes namespace with their status, container states, and restart counts."""
    if not _K8S_AVAILABLE or _core_v1 is None:
        return json.dumps({"error": "Kubernetes client not available", "fallback": "running outside cluster"})
    try:
        pods = _core_v1.list_namespaced_pod(namespace)
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


async def list_deployments(namespace: str = "default") -> str:
    """List all deployments in a Kubernetes namespace with replica counts and health status."""
    if not _K8S_AVAILABLE or _apps_v1 is None:
        return json.dumps({"error": "Kubernetes client not available", "fallback": "running outside cluster"})
    try:
        deps = _apps_v1.list_namespaced_deployment(namespace)
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


async def get_events(namespace: str = "default") -> str:
    """Get recent Kubernetes events in a namespace, especially warnings that indicate problems."""
    if not _K8S_AVAILABLE or _core_v1 is None:
        return json.dumps({"error": "Kubernetes client not available", "fallback": "running outside cluster"})
    try:
        events = _core_v1.list_namespaced_event(namespace)
        results = []
        for event in sorted(
            events.items,
            key=lambda e: e.last_timestamp or e.event_time or "",
            reverse=True,
        )[:15]:
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


# ============================================================
# AutoGen agent construction
# ============================================================

def _make_agent() -> AssistantAgent:
    """Build an AssistantAgent with Anthropic model client and K8s tools."""
    model_client = AnthropicChatCompletionClient(
        model=MODEL_VERSION,
        api_key=os.getenv("ANTHROPIC_API_KEY", ""),
        max_tokens=2048,
    )
    return AssistantAgent(
        name="k8s_health_agent",
        model_client=model_client,
        tools=[list_pods, list_deployments, get_events],
        system_message=SYSTEM_PROMPT,
    )


async def _run_agent_async(question: str) -> tuple[str, int]:
    """Run the AutoGen agent asynchronously and return (answer, tool_calls_count)."""
    agent = _make_agent()
    cancellation_token = CancellationToken()

    tool_calls_count = 0
    final_answer = ""

    # Stream messages from the agent until it produces a final TextMessage
    async for message in agent.on_messages_stream(
        [TextMessage(content=question, source="user")],
        cancellation_token=cancellation_token,
    ):
        # on_messages_stream yields Response or inner messages
        from autogen_agentchat.base import Response
        if isinstance(message, Response):
            # The Response.chat_message is the final answer
            chat_msg = message.chat_message
            if hasattr(chat_msg, "content") and isinstance(chat_msg.content, str):
                final_answer = chat_msg.content
            # Count tool calls from inner_messages
            if message.inner_messages:
                for inner in message.inner_messages:
                    if hasattr(inner, "content") and isinstance(inner.content, list):
                        for item in inner.content:
                            if hasattr(item, "type") and item.type == "function_call":
                                tool_calls_count += 1

    return final_answer, tool_calls_count


def run_agent(question: str) -> tuple[str, int]:
    """Synchronous wrapper around the async AutoGen agent invocation."""
    logger.info("Agent query: %s | prompt=%s model=%s", question, PROMPT_VERSION, MODEL_VERSION)
    try:
        answer, tool_calls_count = asyncio.run(_run_agent_async(question))
    except RuntimeError:
        # If an event loop is already running (e.g., in Jupyter), use nest_asyncio approach
        loop = asyncio.new_event_loop()
        try:
            answer, tool_calls_count = loop.run_until_complete(_run_agent_async(question))
        finally:
            loop.close()
    logger.info("Agent done | tool_calls=%d", tool_calls_count)
    return answer, tool_calls_count


# ============================================================
# FastAPI app
# ============================================================

app = FastAPI(
    title="K8s Health Agent (AutoGen)",
    description="AutoGen-powered Kubernetes cluster health diagnostics — AgentRoll dogfooding example",
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
    """Ask the AutoGen agent a question about cluster health."""
    try:
        answer, tool_calls_count = run_agent(req.question)
        return QueryResponse(
            answer=answer,
            prompt_version=PROMPT_VERSION,
            model_version=MODEL_VERSION,
            tool_calls_count=tool_calls_count,
        )
    except Exception as e:
        logger.error("Agent error: %s", e)
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/")
def root():
    return {
        "name": "autogen-agent",
        "framework": "autogen",
        "prompt_version": PROMPT_VERSION,
        "model_version": MODEL_VERSION,
        "endpoints": ["/query", "/healthz"],
    }
