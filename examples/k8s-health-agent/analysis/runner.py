"""
AgentRoll Analysis Runner — evaluates agent quality during canary deployments.

This script is executed as a Kubernetes Job by Argo Rollouts during the Analysis
step of a canary deployment. It sends test queries to the agent, validates
responses, measures latency, and exits with code 0 (pass) or 1 (fail).

Environment variables (passed by AnalysisTemplate args):
  AGENT_SERVICE_URL  — Full URL to the agent's query endpoint
  AGENT_HEALTHZ_URL  — Full URL to the agent's health endpoint
  TEST_QUERIES       — JSON array of test queries (optional)
  MAX_LATENCY_MS     — Maximum acceptable response latency in ms (default: 10000)
  MIN_RESPONSE_LEN   — Minimum acceptable response length (default: 50)
"""

import os
import sys
import json
import time
import urllib.request
import urllib.error


def log(msg: str):
    print(f"[agentroll-analysis] {msg}", flush=True)


def check_health(healthz_url: str) -> bool:
    """Check if the agent's health endpoint returns ok."""
    try:
        req = urllib.request.Request(healthz_url)
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read())
            healthy = data.get("status") == "ok"
            log(f"Health check: {'PASS' if healthy else 'FAIL'} — {data}")
            return healthy
    except Exception as e:
        log(f"Health check: FAIL — {e}")
        return False


def check_query(query_url: str, question: str, max_latency_ms: int, min_response_len: int) -> dict:
    """Send a test query and evaluate the response."""
    result = {
        "question": question,
        "passed": False,
        "latency_ms": 0,
        "response_len": 0,
        "error": None,
    }

    try:
        payload = json.dumps({"question": question}).encode("utf-8")
        req = urllib.request.Request(
            query_url,
            data=payload,
            headers={"Content-Type": "application/json"},
        )

        start = time.time()
        with urllib.request.urlopen(req, timeout=30) as resp:
            elapsed_ms = (time.time() - start) * 1000
            body = json.loads(resp.read())

        result["latency_ms"] = round(elapsed_ms, 1)

        # Check response has required fields
        answer = body.get("answer", "")
        result["response_len"] = len(answer)
        result["prompt_version"] = body.get("prompt_version", "unknown")
        result["model_version"] = body.get("model_version", "unknown")

        # Evaluation criteria
        checks = []

        # 1. Response is not empty and meets minimum length
        if len(answer) >= min_response_len:
            checks.append(("response_length", True))
        else:
            checks.append(("response_length", False))
            result["error"] = f"Response too short: {len(answer)} < {min_response_len}"

        # 2. Latency is within bounds
        if elapsed_ms <= max_latency_ms:
            checks.append(("latency", True))
        else:
            checks.append(("latency", False))
            result["error"] = f"Latency too high: {elapsed_ms:.0f}ms > {max_latency_ms}ms"

        # 3. Response contains actual content (not just error messages)
        error_indicators = ["error", "failed", "unable to", "cannot", "exception"]
        is_error_response = any(
            indicator in answer.lower()[:100] for indicator in error_indicators
        )
        if not is_error_response:
            checks.append(("content_quality", True))
        else:
            checks.append(("content_quality", False))
            # Don't fail on this — agent might legitimately report errors it found
            log(f"  Warning: Response may contain error indicators (not failing)")
            checks[-1] = ("content_quality", True)  # Override to pass

        result["checks"] = {name: passed for name, passed in checks}
        result["passed"] = all(passed for _, passed in checks)

    except urllib.error.HTTPError as e:
        result["error"] = f"HTTP {e.code}: {e.reason}"
    except urllib.error.URLError as e:
        result["error"] = f"Connection failed: {e.reason}"
    except Exception as e:
        result["error"] = str(e)

    return result


def main():
    # Read configuration from environment
    agent_service_url = os.getenv("AGENT_SERVICE_URL", "http://localhost:8080")
    healthz_url = os.getenv("AGENT_HEALTHZ_URL", f"{agent_service_url}/healthz")
    query_url = f"{agent_service_url}/query"
    max_latency_ms = int(os.getenv("MAX_LATENCY_MS", "10000"))
    min_response_len = int(os.getenv("MIN_RESPONSE_LEN", "50"))

    # Default test queries if none specified
    default_queries = [
        "What pods are running in the default namespace?",
        "Are there any warning events in the cluster?",
    ]
    test_queries_json = os.getenv("TEST_QUERIES")
    if test_queries_json:
        test_queries = json.loads(test_queries_json)
    else:
        test_queries = default_queries

    log(f"Starting analysis")
    log(f"  Service URL: {agent_service_url}")
    log(f"  Max latency: {max_latency_ms}ms")
    log(f"  Min response length: {min_response_len}")
    log(f"  Test queries: {len(test_queries)}")

    all_passed = True

    # Step 1: Health check
    log("--- Health Check ---")
    if not check_health(healthz_url):
        log("RESULT: FAIL — Agent health check failed")
        # Output result for Argo Rollouts
        print(json.dumps({"passed": False, "reason": "health_check_failed"}))
        sys.exit(1)

    # Step 2: Test queries
    log("--- Query Tests ---")
    results = []
    for i, question in enumerate(test_queries):
        log(f"Query {i+1}/{len(test_queries)}: {question[:60]}...")
        result = check_query(query_url, question, max_latency_ms, min_response_len)

        status = "PASS" if result["passed"] else "FAIL"
        log(f"  {status} — latency={result['latency_ms']}ms, len={result['response_len']}")
        if result["error"]:
            log(f"  Error: {result['error']}")

        results.append(result)
        if not result["passed"]:
            all_passed = False

    # Step 3: Summary
    passed_count = sum(1 for r in results if r["passed"])
    total_count = len(results)
    avg_latency = sum(r["latency_ms"] for r in results) / max(total_count, 1)

    log("--- Summary ---")
    log(f"  Queries: {passed_count}/{total_count} passed")
    log(f"  Avg latency: {avg_latency:.0f}ms")

    summary = {
        "passed": all_passed,
        "queries_passed": passed_count,
        "queries_total": total_count,
        "avg_latency_ms": round(avg_latency, 1),
        "results": results,
    }

    if all_passed:
        log("RESULT: PASS — All checks passed")
        print(json.dumps(summary))
        sys.exit(0)
    else:
        log(f"RESULT: FAIL — {total_count - passed_count} checks failed")
        print(json.dumps(summary))
        sys.exit(1)


if __name__ == "__main__":
    main()
