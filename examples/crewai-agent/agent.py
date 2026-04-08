"""
K8s Health Crew — CrewAI dogfooding example for AgentRoll.

Two-agent crew: a Collector gathers raw K8s data using tools,
then an Analyst interprets it and produces a concise health report.

This demonstrates AgentRoll's framework-agnostic deployment with a
multi-agent architecture instead of a single-agent tool loop.

Example questions:
  - "What's the status of my deployments?"
  - "Are there any pods in CrashLoopBackOff?"
  - "How's the cluster looking overall?"
"""

import os
import json
import logging

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

# CrewAI imports
from crewai import Agent, Task, Crew, LLM
from crewai.tools import tool

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

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("crewai-agent")

# ============================================================
# Tools (CrewAI @tool decorator)
# ============================================================

@tool("list_pods")
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


@tool("list_deployments")
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


@tool("get_events")
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
# CrewAI agent + crew construction
# ============================================================

def _make_llm() -> LLM:
    return LLM(
        model=f"anthropic/{MODEL_VERSION}",
        api_key=os.getenv("ANTHROPIC_API_KEY", ""),
        max_tokens=2048,
    )


def _build_crew(question: str) -> Crew:
    """Construct a fresh Crew for each request (CrewAI crews are not reentrant)."""
    llm = _make_llm()

    collector = Agent(
        role="Kubernetes Data Collector",
        goal="Gather comprehensive, accurate raw data from the Kubernetes cluster using available tools.",
        backstory=(
            "You are an expert Kubernetes operator who methodically queries cluster APIs "
            "to collect pod states, deployment health, and recent events. You always use "
            "tools to fetch real data and return it without interpretation."
        ),
        tools=[list_pods, list_deployments, get_events],
        llm=llm,
        verbose=False,
        allow_delegation=False,
    )

    analyst = Agent(
        role="Health Analyst",
        goal="Interpret raw Kubernetes data and produce a concise, actionable health report.",
        backstory=(
            "You are a site reliability engineer who excels at reading Kubernetes telemetry "
            "and translating it into clear diagnoses. You focus on root causes and next steps, "
            "not raw numbers."
        ),
        tools=[],
        llm=llm,
        verbose=False,
        allow_delegation=False,
    )

    collect_task = Task(
        description=(
            f"The user asked: '{question}'\n\n"
            "Use list_pods, list_deployments, and get_events tools to gather all relevant "
            "cluster data from the 'default' namespace. Return the raw JSON results."
        ),
        expected_output="Raw JSON data from list_pods, list_deployments, and get_events tool calls.",
        agent=collector,
    )

    analyze_task = Task(
        description=(
            f"The user asked: '{question}'\n\n"
            "Given the raw Kubernetes data collected in the previous task, produce a concise "
            "health report. Identify what is healthy, what is not, root causes of any issues, "
            "and concrete next steps. Be direct and actionable."
        ),
        expected_output=(
            "A clear, concise health report answering the user's question with diagnosis "
            "and recommended next steps. At least 50 words."
        ),
        agent=analyst,
        context=[collect_task],
    )

    return Crew(
        agents=[collector, analyst],
        tasks=[collect_task, analyze_task],
        verbose=False,
    )


def run_agent(question: str) -> tuple[str, int]:
    """Run the CrewAI crew and return (answer, tool_calls_count)."""
    logger.info("Crew query: %s | prompt=%s model=%s", question, PROMPT_VERSION, MODEL_VERSION)

    crew = _build_crew(question)
    result = crew.kickoff(inputs={"question": question})

    # CrewAI result may be a string or an object with .raw
    if hasattr(result, "raw"):
        answer = result.raw
    else:
        answer = str(result)

    # Count tool usage from task outputs if available
    tool_calls_count = 0
    try:
        for task_output in crew.tasks:
            if hasattr(task_output, "output") and task_output.output:
                # Heuristic: collector uses 3 tools by default
                if task_output.agent.role == "Kubernetes Data Collector":
                    tool_calls_count += 3
    except Exception:
        tool_calls_count = 3  # collector runs 3 tools

    logger.info("Crew done | tool_calls_approx=%d", tool_calls_count)
    return answer, tool_calls_count


# ============================================================
# FastAPI app
# ============================================================

app = FastAPI(
    title="K8s Health Crew (CrewAI)",
    description="CrewAI two-agent Kubernetes health crew — AgentRoll dogfooding example",
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
    """Ask the CrewAI crew a question about cluster health."""
    try:
        answer, tool_calls_count = run_agent(req.question)
        return QueryResponse(
            answer=answer,
            prompt_version=PROMPT_VERSION,
            model_version=MODEL_VERSION,
            tool_calls_count=tool_calls_count,
        )
    except Exception as e:
        logger.error("Crew error: %s", e)
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/")
def root():
    return {
        "name": "crewai-agent",
        "framework": "crewai",
        "prompt_version": PROMPT_VERSION,
        "model_version": MODEL_VERSION,
        "endpoints": ["/query", "/healthz"],
    }
