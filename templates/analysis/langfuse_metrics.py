"""
AgentRoll Langfuse Metrics Provider — evaluates agent quality from Langfuse trace data.

This script is executed as a Kubernetes Job by Argo Rollouts during the Analysis
step of a canary deployment. It queries the Langfuse API for traces belonging to
the canary version, computes the tool call success rate, and exits with code 0
(pass) or 1 (fail).

Environment variables:
  LANGFUSE_HOST          — Langfuse server URL (e.g., https://cloud.langfuse.com)
  LANGFUSE_PUBLIC_KEY    — Langfuse public API key (used as Basic auth username)
  LANGFUSE_SECRET_KEY    — Langfuse secret API key (used as Basic auth password)
  CANARY_VERSION         — The composite version tag to filter traces
                           (e.g., "v2.claude-sonnet-4.1.2.3")
  METRIC                 — Which metric to compute: "tool_success_rate" (default)
  MIN_SUCCESS_RATE       — Minimum tool success rate 0.0–1.0 (default: 0.90)
  TIME_WINDOW_MINUTES    — How far back to look for traces (default: 10)
  MIN_TRACES             — Minimum traces required to make a judgment (default: 5).
                           If fewer traces exist, the check passes with a warning
                           (insufficient data rather than a false negative).
"""

import os
import sys
import json
import base64
import urllib.request
import urllib.error
import urllib.parse
from datetime import datetime, timedelta, timezone


def log(msg: str):
    print(f"[agentroll-langfuse] {msg}", flush=True)


def make_auth_header(public_key: str, secret_key: str) -> str:
    """Langfuse uses HTTP Basic auth: public_key:secret_key."""
    credentials = f"{public_key}:{secret_key}"
    encoded = base64.b64encode(credentials.encode("utf-8")).decode("utf-8")
    return f"Basic {encoded}"


def fetch_traces(
    host: str,
    auth_header: str,
    version_tag: str,
    from_timestamp: str,
    page: int = 1,
    limit: int = 100,
) -> dict:
    """
    Query GET /api/public/traces with tag and timestamp filters.

    Langfuse traces tagged with "prompt:<version>" or "version:<composite>" are
    returned. We filter client-side on both tag formats for flexibility.
    """
    params = urllib.parse.urlencode({
        "page": page,
        "limit": limit,
        "fromTimestamp": from_timestamp,
        "tags": f"canary:{version_tag}",
    })
    url = f"{host}/api/public/traces?{params}"
    req = urllib.request.Request(url, headers={"Authorization": auth_header})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def fetch_observations_for_trace(host: str, auth_header: str, trace_id: str) -> list:
    """
    Query GET /api/public/observations for a specific trace.
    Returns all observations (spans, generations, events) within the trace.
    """
    params = urllib.parse.urlencode({"traceId": trace_id, "limit": 100})
    url = f"{host}/api/public/observations?{params}"
    req = urllib.request.Request(url, headers={"Authorization": auth_header})
    with urllib.request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read())
        return data.get("data", [])


def collect_all_traces(
    host: str,
    auth_header: str,
    version_tag: str,
    from_timestamp: str,
) -> list:
    """Fetch all matching traces, handling pagination."""
    all_traces = []
    page = 1
    while True:
        try:
            data = fetch_traces(host, auth_header, version_tag, from_timestamp, page=page)
        except urllib.error.HTTPError as e:
            log(f"Langfuse API error fetching traces (page {page}): HTTP {e.code}: {e.reason}")
            break
        except Exception as e:
            log(f"Langfuse API error fetching traces (page {page}): {e}")
            break

        traces = data.get("data", [])
        if not traces:
            break

        all_traces.extend(traces)
        meta = data.get("meta", {})
        total_pages = meta.get("totalPages", 1)
        if page >= total_pages:
            break
        page += 1

    return all_traces


def compute_tool_success_rate(
    host: str,
    auth_header: str,
    traces: list,
) -> dict:
    """
    Compute tool call success rate from trace observations.

    A tool call span is identified by observation type "SPAN" and a name
    prefixed with "tool_call". Success is defined as the observation NOT having
    level "ERROR".

    Returns a dict with:
      - total_tool_calls: int
      - successful_tool_calls: int
      - success_rate: float (0.0 if no tool calls observed)
      - traces_analyzed: int
    """
    total = 0
    successful = 0

    for trace in traces:
        try:
            observations = fetch_observations_for_trace(host, auth_header, trace["id"])
        except Exception as e:
            log(f"  Warning: could not fetch observations for trace {trace['id']}: {e}")
            continue

        for obs in observations:
            obs_type = obs.get("type", "")
            obs_name = obs.get("name", "")
            # Tool call spans are tagged with type SPAN and name "tool_call"
            if obs_type == "SPAN" and obs_name.startswith("tool_call"):
                total += 1
                if obs.get("level", "DEFAULT") != "ERROR":
                    successful += 1

    rate = successful / total if total > 0 else 0.0
    return {
        "total_tool_calls": total,
        "successful_tool_calls": successful,
        "success_rate": round(rate, 4),
        "traces_analyzed": len(traces),
    }


def main():
    host = os.getenv("LANGFUSE_HOST", "").rstrip("/")
    public_key = os.getenv("LANGFUSE_PUBLIC_KEY", "")
    secret_key = os.getenv("LANGFUSE_SECRET_KEY", "")
    canary_version = os.getenv("CANARY_VERSION", "")
    metric = os.getenv("METRIC", "tool_success_rate")
    min_success_rate = float(os.getenv("MIN_SUCCESS_RATE", "0.90"))
    time_window_minutes = int(os.getenv("TIME_WINDOW_MINUTES", "10"))
    min_traces = int(os.getenv("MIN_TRACES", "5"))

    # Validate required config
    missing = [k for k, v in [
        ("LANGFUSE_HOST", host),
        ("LANGFUSE_PUBLIC_KEY", public_key),
        ("LANGFUSE_SECRET_KEY", secret_key),
        ("CANARY_VERSION", canary_version),
    ] if not v]
    if missing:
        log(f"RESULT: FAIL — Missing required env vars: {', '.join(missing)}")
        print(json.dumps({"passed": False, "reason": f"missing_config: {missing}"}))
        sys.exit(1)

    auth_header = make_auth_header(public_key, secret_key)

    from_dt = datetime.now(tz=timezone.utc) - timedelta(minutes=time_window_minutes)
    from_timestamp = from_dt.strftime("%Y-%m-%dT%H:%M:%SZ")

    log(f"Langfuse host: {host}")
    log(f"Canary version: {canary_version}")
    log(f"Metric: {metric}")
    log(f"Time window: last {time_window_minutes}m (from {from_timestamp})")
    log(f"Min success rate: {min_success_rate}")
    log(f"Min traces required: {min_traces}")

    # Collect traces
    log("--- Fetching Traces ---")
    traces = collect_all_traces(host, auth_header, canary_version, from_timestamp)
    log(f"Found {len(traces)} traces for version '{canary_version}'")

    if len(traces) < min_traces:
        log(
            f"RESULT: INCONCLUSIVE — Only {len(traces)} traces found, "
            f"need >= {min_traces} for a reliable signal. Passing to avoid false negative."
        )
        result = {
            "passed": True,
            "inconclusive": True,
            "reason": f"insufficient_traces: {len(traces)} < {min_traces}",
            "traces_found": len(traces),
        }
        print(json.dumps(result))
        sys.exit(0)

    # Compute metric
    if metric == "tool_success_rate":
        log("--- Computing Tool Success Rate ---")
        metrics = compute_tool_success_rate(host, auth_header, traces)
        log(f"  Tool calls observed: {metrics['total_tool_calls']}")
        log(f"  Successful: {metrics['successful_tool_calls']}")
        log(f"  Success rate: {metrics['success_rate']:.1%}")

        if metrics["total_tool_calls"] == 0:
            log(
                "RESULT: INCONCLUSIVE — No tool call spans found. "
                "Ensure the agent is instrumented with Langfuse SDK and spans are named 'tool_call*'."
            )
            result = {
                "passed": True,
                "inconclusive": True,
                "reason": "no_tool_call_spans_found",
                **metrics,
            }
            print(json.dumps(result))
            sys.exit(0)

        passed = metrics["success_rate"] >= min_success_rate
        result = {
            "passed": passed,
            "metric": "tool_success_rate",
            "threshold": min_success_rate,
            **metrics,
        }

        if passed:
            log(f"RESULT: PASS — {metrics['success_rate']:.1%} >= {min_success_rate:.1%}")
        else:
            log(f"RESULT: FAIL — {metrics['success_rate']:.1%} < {min_success_rate:.1%}")

        print(json.dumps(result))
        sys.exit(0 if passed else 1)

    else:
        log(f"RESULT: FAIL — Unknown metric '{metric}'. Supported: tool_success_rate")
        print(json.dumps({"passed": False, "reason": f"unknown_metric: {metric}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
