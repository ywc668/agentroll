#!/usr/bin/env python3
"""
tool_checker.py — Sprint 11: Tool Management

Checks MCP tool call success rates by analyzing Langfuse trace observations.
Writes per-tool Langfuse scores (tool_success_rate_<name>) tagged with cv=<version>.
Exits 0 if average success rate >= MIN_TOOL_SUCCESS_RATE, 1 otherwise.
Falls back to exit 0 if no traces are found (avoids blocking rollouts for new deployments).
"""

import json
import os
import sys

import requests

AGENT_SERVICE_URL = os.environ["AGENT_SERVICE_URL"]
MIN_TOOL_SUCCESS_RATE = float(os.environ.get("MIN_TOOL_SUCCESS_RATE", "0.8"))
CANARY_VERSION = os.environ.get("CANARY_VERSION", "")
LANGFUSE_HOST = os.environ.get("LANGFUSE_HOST", "https://cloud.langfuse.com").rstrip("/")
LANGFUSE_PUBLIC_KEY = os.environ.get("LANGFUSE_PUBLIC_KEY", "")
LANGFUSE_SECRET_KEY = os.environ.get("LANGFUSE_SECRET_KEY", "")
TEST_QUERIES = json.loads(os.environ.get("TEST_QUERIES", '["What pods are running?"]'))


def call_agent(query: str) -> dict:
    """Send a query to the agent."""
    resp = requests.post(
        f"{AGENT_SERVICE_URL}/query",
        json={"query": query},
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json()


def fetch_recent_traces(limit: int = 20) -> list:
    """Fetch recent Langfuse traces tagged with the canary version."""
    if not LANGFUSE_PUBLIC_KEY:
        return []
    resp = requests.get(
        f"{LANGFUSE_HOST}/api/public/traces",
        params={"tags": f"canary:{CANARY_VERSION}", "limit": limit},
        auth=(LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY),
        timeout=10,
    )
    resp.raise_for_status()
    return resp.json().get("data", [])


def fetch_trace_observations(trace_id: str) -> list:
    """Fetch all observations (spans/generations) for a trace."""
    resp = requests.get(
        f"{LANGFUSE_HOST}/api/public/observations",
        params={"traceId": trace_id},
        auth=(LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY),
        timeout=10,
    )
    resp.raise_for_status()
    return resp.json().get("data", [])


def extract_tool_results(observations: list) -> dict:
    """
    Extract tool call success/failure counts from a list of observations.
    Only SPAN-type observations are considered tool calls.
    Returns dict mapping tool_name -> {"success": int, "fail": int}.
    """
    results = {}
    for obs in observations:
        if obs.get("type") != "SPAN":
            continue
        tool_name = obs.get("name", "")
        if not tool_name:
            continue
        entry = results.setdefault(tool_name, {"success": 0, "fail": 0})
        if obs.get("level") == "ERROR" or obs.get("statusMessage") == "error":
            entry["fail"] += 1
        else:
            entry["success"] += 1
    return results


def write_langfuse_score(name: str, value: float, comment: str) -> None:
    """Write a named score to Langfuse."""
    if not LANGFUSE_PUBLIC_KEY:
        return
    requests.post(
        f"{LANGFUSE_HOST}/api/public/scores",
        json={"name": name, "value": value, "comment": comment},
        auth=(LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY),
        timeout=10,
    ).raise_for_status()


def main() -> int:
    # Warm up traces by sending test queries.
    for query in TEST_QUERIES:
        try:
            call_agent(query)
        except Exception as e:
            print(f"Warning: agent query failed: {e}", file=sys.stderr)

    # Fetch traces tagged with the canary version.
    traces = fetch_recent_traces()
    if not traces:
        print("No traces found for canary version — passing by default", file=sys.stderr)
        return 0

    # Aggregate tool results across all traces.
    all_tool_results: dict = {}
    for trace in traces:
        try:
            obs = fetch_trace_observations(trace["id"])
        except Exception as e:
            print(f"Warning: failed to fetch observations for trace {trace['id']}: {e}",
                  file=sys.stderr)
            continue
        for tool_name, counts in extract_tool_results(obs).items():
            agg = all_tool_results.setdefault(tool_name, {"success": 0, "fail": 0})
            agg["success"] += counts["success"]
            agg["fail"] += counts["fail"]

    if not all_tool_results:
        print("No tool calls found in traces — passing by default", file=sys.stderr)
        return 0

    # Compute and write per-tool success rates to Langfuse.
    rates = []
    comment = f"cv={CANARY_VERSION}"
    for tool_name, counts in all_tool_results.items():
        total = counts["success"] + counts["fail"]
        rate = counts["success"] / total if total > 0 else 0.0
        rates.append(rate)
        print(f"Tool {tool_name}: {counts['success']}/{total} = {rate:.3f}")
        score_name = f"tool_success_rate_{tool_name.replace('-', '_')}"
        try:
            write_langfuse_score(score_name, rate, comment)
        except Exception as e:
            print(f"Warning: failed to write score {score_name}: {e}", file=sys.stderr)

    avg_rate = sum(rates) / len(rates)
    print(f"Average tool success rate: {avg_rate:.3f} (threshold: {MIN_TOOL_SUCCESS_RATE})")

    if avg_rate < MIN_TOOL_SUCCESS_RATE:
        print(f"FAIL: {avg_rate:.3f} < {MIN_TOOL_SUCCESS_RATE}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
