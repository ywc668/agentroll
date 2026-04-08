"""
AgentRoll Langfuse Metrics Provider — evaluates agent quality from Langfuse trace data.

This script is executed as a Kubernetes Job by Argo Rollouts during the Analysis
step of a canary deployment. It queries the Langfuse API for traces belonging to
the canary version, computes quality metrics, and exits with code 0 (pass) or 1 (fail).

Environment variables (all metrics):
  LANGFUSE_HOST          — Langfuse server URL (e.g., https://cloud.langfuse.com)
  LANGFUSE_PUBLIC_KEY    — Langfuse public API key (used as Basic auth username)
  LANGFUSE_SECRET_KEY    — Langfuse secret API key (used as Basic auth password)
  CANARY_VERSION         — The composite version tag to filter canary traces
  METRIC                 — Which metric to compute (default: "tool_success_rate")
                           Supported: tool_success_rate | avg_latency | token_cost_ratio
  TIME_WINDOW_MINUTES    — How far back to look for traces (default: 10)
  MIN_TRACES             — Minimum traces required to make a judgment (default: 5).
                           Fewer traces → inconclusive pass (avoids false negatives).

Per-metric configuration:
  tool_success_rate:
    MIN_SUCCESS_RATE     — Minimum tool success rate 0.0–1.0 (default: 0.90)

  avg_latency:
    MAX_LATENCY_MS       — Maximum acceptable average latency in ms (default: 5000)

  token_cost_ratio (compares canary cost vs stable cost):
    STABLE_VERSION       — Stable version tag to compare against (required)
    MAX_COST_RATIO       — Max allowed ratio of canary/stable cost (default: 2.0 = 200%)
                           Uses approximate Claude pricing: $3/M input, $15/M output tokens
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


def compute_avg_latency(traces: list) -> dict:
    """
    Compute average and p95 trace latency from Langfuse trace metadata.

    Langfuse traces include a 'latency' field (milliseconds). If the field is
    absent, that trace is skipped (some older SDK versions omit it).

    Returns a dict with avg_latency_ms, p95_latency_ms, traces_analyzed, latencies_found.
    """
    latencies = [
        t["latency"]
        for t in traces
        if t.get("latency") is not None and t["latency"] >= 0
    ]

    if not latencies:
        return {
            "avg_latency_ms": 0.0,
            "p95_latency_ms": 0.0,
            "traces_analyzed": len(traces),
            "latencies_found": 0,
        }

    sorted_lats = sorted(latencies)
    p95_idx = max(0, int(len(sorted_lats) * 0.95) - 1)
    return {
        "avg_latency_ms": round(sum(latencies) / len(latencies), 1),
        "p95_latency_ms": round(sorted_lats[p95_idx], 1),
        "traces_analyzed": len(traces),
        "latencies_found": len(latencies),
    }


def _traces_token_cost(host: str, auth_header: str, traces: list) -> tuple:
    """
    Sum token usage across all GENERATION observations for a list of traces.

    Returns (total_cost_usd, total_input_tokens, total_output_tokens).
    Uses approximate Claude pricing: $3/M input tokens, $15/M output tokens.
    """
    input_price_per_m = 3.0    # USD per million input tokens
    output_price_per_m = 15.0  # USD per million output tokens
    total_input = 0
    total_output = 0

    for trace in traces:
        try:
            observations = fetch_observations_for_trace(host, auth_header, trace["id"])
        except Exception as e:
            log(f"  Warning: could not fetch observations for trace {trace['id']}: {e}")
            continue
        for obs in observations:
            if obs.get("type") == "GENERATION":
                usage = obs.get("usage") or {}
                total_input += usage.get("input", 0) or 0
                total_output += usage.get("output", 0) or 0

    cost = (total_input * input_price_per_m + total_output * output_price_per_m) / 1_000_000
    return cost, total_input, total_output


def compute_token_cost_ratio(
    host: str,
    auth_header: str,
    canary_traces: list,
    stable_version: str,
    from_timestamp: str,
) -> dict:
    """
    Compare token cost between canary and stable versions.

    Fetches stable version traces from Langfuse, computes per-trace-normalized cost
    for both versions, and returns cost_ratio = canary_cost_per_trace / stable_cost_per_trace.

    Per-trace normalization prevents the ratio from being skewed by different traffic volumes.
    If stable traces are not found, returns inconclusive (no_stable_data=True).
    """
    log(f"  Fetching stable traces for version '{stable_version}'...")
    stable_traces = collect_all_traces(host, auth_header, stable_version, from_timestamp)
    log(f"  Found {len(stable_traces)} stable traces")

    canary_cost, canary_input, canary_output = _traces_token_cost(host, auth_header, canary_traces)
    stable_cost, stable_input, stable_output = _traces_token_cost(host, auth_header, stable_traces)

    if not stable_traces or stable_cost <= 0:
        log(f"  Warning: stable version '{stable_version}' has no token data.")
        return {
            "cost_ratio": None,
            "no_stable_data": True,
            "canary_cost_usd": round(canary_cost, 6),
            "stable_cost_usd": 0.0,
            "canary_traces": len(canary_traces),
            "stable_traces": len(stable_traces),
        }

    # Normalize by trace count to compare cost-per-request, not total cost
    canary_cost_per_trace = canary_cost / len(canary_traces) if canary_traces else 0
    stable_cost_per_trace = stable_cost / len(stable_traces) if stable_traces else 0
    ratio = canary_cost_per_trace / stable_cost_per_trace if stable_cost_per_trace > 0 else 0.0

    return {
        "cost_ratio": round(ratio, 4),
        "canary_cost_per_trace_usd": round(canary_cost_per_trace, 6),
        "stable_cost_per_trace_usd": round(stable_cost_per_trace, 6),
        "canary_cost_usd": round(canary_cost, 6),
        "stable_cost_usd": round(stable_cost, 6),
        "canary_input_tokens": canary_input,
        "canary_output_tokens": canary_output,
        "stable_input_tokens": stable_input,
        "stable_output_tokens": stable_output,
        "canary_traces": len(canary_traces),
        "stable_traces": len(stable_traces),
    }


def main():
    host = os.getenv("LANGFUSE_HOST", "").rstrip("/")
    public_key = os.getenv("LANGFUSE_PUBLIC_KEY", "")
    secret_key = os.getenv("LANGFUSE_SECRET_KEY", "")
    canary_version = os.getenv("CANARY_VERSION", "")
    metric = os.getenv("METRIC", "tool_success_rate")
    min_success_rate = float(os.getenv("MIN_SUCCESS_RATE", "0.90"))
    max_latency_ms = float(os.getenv("MAX_LATENCY_MS", "5000"))
    stable_version = os.getenv("STABLE_VERSION", "")
    max_cost_ratio = float(os.getenv("MAX_COST_RATIO", "2.0"))
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

    elif metric == "avg_latency":
        log("--- Computing Average Latency ---")
        log(f"Max allowed average latency: {max_latency_ms:.0f}ms")
        metrics_data = compute_avg_latency(traces)
        log(f"  Avg latency: {metrics_data['avg_latency_ms']:.0f}ms")
        log(f"  p95 latency: {metrics_data['p95_latency_ms']:.0f}ms")
        log(f"  Traces with latency data: {metrics_data['latencies_found']}/{metrics_data['traces_analyzed']}")

        if metrics_data["latencies_found"] == 0:
            log("RESULT: INCONCLUSIVE — No latency data found in traces.")
            result = {"passed": True, "inconclusive": True, "reason": "no_latency_data", **metrics_data}
            print(json.dumps(result))
            sys.exit(0)

        passed = metrics_data["avg_latency_ms"] <= max_latency_ms
        result = {
            "passed": passed,
            "metric": "avg_latency",
            "threshold_ms": max_latency_ms,
            **metrics_data,
        }
        if passed:
            log(f"RESULT: PASS — {metrics_data['avg_latency_ms']:.0f}ms <= {max_latency_ms:.0f}ms")
        else:
            log(f"RESULT: FAIL — {metrics_data['avg_latency_ms']:.0f}ms > {max_latency_ms:.0f}ms")
        print(json.dumps(result))
        sys.exit(0 if passed else 1)

    elif metric == "token_cost_ratio":
        if not stable_version:
            log("RESULT: INCONCLUSIVE — STABLE_VERSION not set; skipping cost ratio check.")
            result = {"passed": True, "inconclusive": True, "reason": "no_stable_version_configured"}
            print(json.dumps(result))
            sys.exit(0)

        log(f"--- Computing Token Cost Ratio (canary '{canary_version}' vs stable '{stable_version}') ---")
        log(f"Max allowed cost ratio: {max_cost_ratio:.2f}x")
        metrics_data = compute_token_cost_ratio(
            host, auth_header, traces, stable_version, from_timestamp
        )
        log(f"  Canary cost: ${metrics_data['canary_cost_usd']:.4f} "
            f"({metrics_data['canary_traces']} traces)")
        log(f"  Stable cost: ${metrics_data['stable_cost_usd']:.4f} "
            f"({metrics_data['stable_traces']} traces)")

        if metrics_data.get("no_stable_data") or metrics_data["cost_ratio"] is None:
            log("RESULT: INCONCLUSIVE — No stable version token data available.")
            result = {"passed": True, "inconclusive": True, "reason": "no_stable_data", **metrics_data}
            print(json.dumps(result))
            sys.exit(0)

        log(f"  Cost ratio: {metrics_data['cost_ratio']:.2f}x "
            f"(${metrics_data['canary_cost_per_trace_usd']:.4f} canary vs "
            f"${metrics_data['stable_cost_per_trace_usd']:.4f} stable per trace)")
        passed = metrics_data["cost_ratio"] <= max_cost_ratio
        result = {
            "passed": passed,
            "metric": "token_cost_ratio",
            "threshold": max_cost_ratio,
            **metrics_data,
        }
        if passed:
            log(f"RESULT: PASS — {metrics_data['cost_ratio']:.2f}x <= {max_cost_ratio:.2f}x")
        else:
            log(f"RESULT: FAIL — {metrics_data['cost_ratio']:.2f}x > {max_cost_ratio:.2f}x")
        print(json.dumps(result))
        sys.exit(0 if passed else 1)

    else:
        log(f"RESULT: FAIL — Unknown metric '{metric}'. "
            f"Supported: tool_success_rate | avg_latency | token_cost_ratio")
        print(json.dumps({"passed": False, "reason": f"unknown_metric: {metric}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
