"""
K8s Health Agent — LangGraph dogfooding example for AgentRoll.

Demonstrates AgentRoll's framework-agnostic deployment: this agent is
functionally equivalent to the k8s-health-agent but uses LangGraph's
StateGraph for the agent loop instead of a hand-rolled loop.

Example questions:
  - "What's the status of my deployments?"
  - "Are there any pods in CrashLoopBackOff?"
  - "How's the nginx deployment doing?"
"""

import os
import json
import logging
from typing import TypedDict, Annotated
import operator

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

# LangGraph / LangChain imports
from langchain_anthropic import ChatAnthropic
from langchain_core.tools import tool
from langchain_core.messages import HumanMessage, AIMessage, BaseMessage
from langgraph.prebuilt import create_react_agent

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
logger = logging.getLogger("langgraph-agent")

# ============================================================
# LangChain tools (decorated with @tool)
# ============================================================

@tool
def list_pods(namespace: str = "default") -> str:
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


@tool
def list_deployments(namespace: str = "default") -> str:
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


@tool
def get_events(namespace: str = "default") -> str:
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
# LangGraph agent construction
# ============================================================

_llm = ChatAnthropic(
    model=MODEL_VERSION,
    anthropic_api_key=os.getenv("ANTHROPIC_API_KEY", ""),
    max_tokens=2048,
)
_tools = [list_pods, list_deployments, get_events]

# create_react_agent builds a StateGraph with a tool-calling loop internally
_graph = create_react_agent(_llm, tools=_tools, state_modifier=SYSTEM_PROMPT)


def run_agent(question: str) -> tuple[str, int]:
    """Invoke the LangGraph ReAct agent and return (answer, tool_calls_count)."""
    logger.info("Agent query: %s | prompt=%s model=%s", question, PROMPT_VERSION, MODEL_VERSION)

    result = _graph.invoke({"messages": [HumanMessage(content=question)]})

    # Count tool calls by inspecting AIMessages with tool_calls
    tool_calls_count = 0
    for msg in result.get("messages", []):
        if isinstance(msg, AIMessage) and msg.tool_calls:
            tool_calls_count += len(msg.tool_calls)

    # Final answer is the last AIMessage that has no tool_calls
    final_answer = ""
    for msg in reversed(result.get("messages", [])):
        if isinstance(msg, AIMessage) and not msg.tool_calls:
            final_answer = msg.content if isinstance(msg.content, str) else str(msg.content)
            break

    logger.info("Agent done | tool_calls=%d", tool_calls_count)
    return final_answer, tool_calls_count


# ============================================================
# FastAPI app
# ============================================================

app = FastAPI(
    title="K8s Health Agent (LangGraph)",
    description="LangGraph-powered Kubernetes cluster health diagnostics — AgentRoll dogfooding example",
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
    """Ask the LangGraph agent a question about cluster health."""
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
        "name": "langgraph-agent",
        "framework": "langgraph",
        "prompt_version": PROMPT_VERSION,
        "model_version": MODEL_VERSION,
        "endpoints": ["/query", "/healthz"],
    }
