"""
AgentRoll Judge Runner — LLM-as-judge quality evaluation for canary deployments.

Sends test queries to the agent, scores each response using an LLM judge on a
0–10 scale, writes the mean quality score (0.0–1.0) to Langfuse, and exits
0 (pass) if mean >= MIN_JUDGE_SCORE, else 1 (fail).

Environment variables:
  AGENT_SERVICE_URL    — Agent HTTP endpoint (e.g. http://my-agent.default.svc:8080)
  JUDGE_PROVIDER       — "anthropic" or "openai" (default: "anthropic")
  JUDGE_MODEL          — Judge LLM model ID (default: "claude-haiku-4-5-20251001")
  JUDGE_API_KEY        — Judge LLM API key (required)
  MIN_JUDGE_SCORE      — Minimum acceptable mean score 0.0–1.0 (default: "0.7")
  EVAL_RUBRIC          — Evaluation rubric text (optional, default built-in)
  TEST_QUERIES         — JSON array of test input strings (optional)
  CANARY_VERSION       — Composite version string for Langfuse tagging (optional)
  LANGFUSE_HOST        — Langfuse server URL (optional)
  LANGFUSE_PUBLIC_KEY  — Langfuse public key for writing scores (optional)
  LANGFUSE_SECRET_KEY  — Langfuse secret key (optional)
"""

import base64
import json
import os
import sys
import time
import urllib.error
import urllib.request

DEFAULT_RUBRIC = """Evaluate whether the agent's response:
1. Directly answers the question asked (not evasive or off-topic)
2. Is factually grounded (no obvious hallucinations)
3. Is appropriately concise (no excessive padding or repetition)
4. Shows correct use of tools when tools were needed
Score 0 = completely wrong or useless; 10 = excellent, accurate, helpful."""

DEFAULT_QUERIES = [
    "What pods are running in the default namespace?",
    "Are there any warning events in the cluster?",
]


def log(msg: str) -> None:
    print(f"[agentroll-judge] {msg}", flush=True)


# ── Agent querying ────────────────────────────────────────────────────────────

def query_agent(service_url: str, question: str) -> tuple[str, float]:
    """
    POST /query to the agent. Returns (answer_text, latency_ms).
    Raises urllib.error.URLError on network failure.
    """
    query_url = service_url.rstrip("/") + "/query"
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
    answer = body.get("answer", "")
    return answer, round(elapsed_ms, 1)


# ── LLM judge ─────────────────────────────────────────────────────────────────

def parse_judge_score(llm_output: str):
    """
    Extract score int from LLM JSON output.
    Returns int 0–10, or None if parsing fails.
    """
    # Find the first JSON object in the output (LLMs sometimes add prose around it)
    start = llm_output.find("{")
    end = llm_output.rfind("}") + 1
    if start == -1 or end == 0:
        return None
    try:
        data = json.loads(llm_output[start:end])
        score = int(data.get("score", -1))
        return max(0, min(10, score))  # clamp to [0, 10]
    except (json.JSONDecodeError, ValueError, TypeError):
        return None


def call_anthropic(model: str, api_key: str, system: str, user: str) -> str:
    payload = json.dumps({
        "model": model,
        "max_tokens": 256,
        "system": system,
        "messages": [{"role": "user", "content": user}],
    }).encode("utf-8")
    req = urllib.request.Request(
        "https://api.anthropic.com/v1/messages",
        data=payload,
        headers={
            "Content-Type": "application/json",
            "x-api-key": api_key,
            "anthropic-version": "2023-06-01",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = json.loads(resp.read())
    for block in body.get("content", []):
        if block.get("type") == "text":
            return block["text"]
    return ""


def call_openai(model: str, api_key: str, system: str, user: str) -> str:
    payload = json.dumps({
        "model": model,
        "max_tokens": 256,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
    }).encode("utf-8")
    req = urllib.request.Request(
        "https://api.openai.com/v1/chat/completions",
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = json.loads(resp.read())
    choices = body.get("choices", [])
    if choices:
        return choices[0]["message"]["content"]
    return ""


def judge_response(
    provider: str,
    model: str,
    api_key: str,
    rubric: str,
    query: str,
    response: str,
) -> int:
    """
    Ask the LLM judge to score a single (query, response) pair.
    Returns int 0–10, or 5 (neutral) on parse failure.
    """
    system = (
        "You are an AI quality evaluator. Score the agent response strictly based on "
        "the rubric. Return ONLY valid JSON with no extra text: "
        '{"score": <integer 0-10>, "reason": "<one sentence>"}'
    )
    user = (
        f"Rubric:\n{rubric}\n\n"
        f"Query: {query}\n\n"
        f"Agent response:\n{response}\n\n"
        "Score (0=terrible, 10=excellent):"
    )
    try:
        if provider == "openai":
            raw = call_openai(model, api_key, system, user)
        else:
            raw = call_anthropic(model, api_key, system, user)
        score = parse_judge_score(raw)
        if score is None:
            log(f"  Warning: could not parse judge score from: {raw[:80]}")
            return 5
        return score
    except Exception as exc:
        log(f"  Warning: judge LLM call failed: {exc}")
        return 5  # neutral fallback — do not hard-fail on judge errors


def compute_quality_score(raw_scores: list) -> float:
    """Normalise list of 0–10 int scores to 0.0–1.0 float mean."""
    if not raw_scores:
        return 0.0
    return sum(raw_scores) / (len(raw_scores) * 10.0)


# ── Langfuse score write ───────────────────────────────────────────────────────

def build_langfuse_score_payload(quality_score: float, composite_version: str) -> dict:
    return {
        "name": "judge_quality_score",
        "value": quality_score,
        "comment": f"cv={composite_version}",
        "source": "eval",
        "dataType": "NUMERIC",
    }


def write_langfuse_score(
    host: str,
    public_key: str,
    secret_key: str,
    quality_score: float,
    composite_version: str,
) -> None:
    """POST judge_quality_score to Langfuse. Errors are logged but not fatal."""
    payload = build_langfuse_score_payload(quality_score, composite_version)
    body = json.dumps(payload).encode("utf-8")
    credentials = base64.b64encode(f"{public_key}:{secret_key}".encode()).decode()
    url = host.rstrip("/") + "/api/public/scores"
    req = urllib.request.Request(
        url,
        data=body,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Basic {credentials}",
        },
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        status = resp.status
    log(f"Langfuse score written (HTTP {status}): judge_quality_score={quality_score:.4f}")


# ── Main ──────────────────────────────────────────────────────────────────────

def main() -> None:
    service_url = os.getenv("AGENT_SERVICE_URL", "http://localhost:8080")
    judge_provider = os.getenv("JUDGE_PROVIDER", "anthropic")
    judge_model = os.getenv("JUDGE_MODEL", "claude-haiku-4-5-20251001")
    judge_api_key = os.getenv("JUDGE_API_KEY", "")
    min_score = float(os.getenv("MIN_JUDGE_SCORE", "0.7"))
    rubric = os.getenv("EVAL_RUBRIC", DEFAULT_RUBRIC)
    canary_version = os.getenv("CANARY_VERSION", "unknown")

    raw_queries = os.getenv("TEST_QUERIES", "")
    test_queries = json.loads(raw_queries) if raw_queries else DEFAULT_QUERIES

    langfuse_host = os.getenv("LANGFUSE_HOST", "")
    langfuse_public = os.getenv("LANGFUSE_PUBLIC_KEY", "")
    langfuse_secret = os.getenv("LANGFUSE_SECRET_KEY", "")

    if not judge_api_key:
        log("ERROR: JUDGE_API_KEY is required")
        sys.exit(1)

    log(f"Starting judge evaluation")
    log(f"  Service URL:     {service_url}")
    log(f"  Judge:           {judge_provider}/{judge_model}")
    log(f"  Min score:       {min_score}")
    log(f"  Canary version:  {canary_version}")
    log(f"  Test queries:    {len(test_queries)}")
    log(f"  Langfuse sink:   {'yes' if langfuse_host and langfuse_public else 'no'}")

    raw_scores = []

    for i, question in enumerate(test_queries):
        log(f"Query {i + 1}/{len(test_queries)}: {question[:60]}...")
        try:
            answer, latency_ms = query_agent(service_url, question)
        except Exception as exc:
            log(f"  SKIP — agent query failed: {exc}")
            raw_scores.append(0)  # failed response = 0
            continue

        score = judge_response(
            judge_provider, judge_model, judge_api_key, rubric, question, answer
        )
        log(f"  score={score}/10  latency={latency_ms}ms  len={len(answer)}")
        raw_scores.append(score)

    quality_score = compute_quality_score(raw_scores)
    passed = quality_score >= min_score
    verdict = "PASS" if passed else "FAIL"

    log(f"--- Summary ---")
    log(f"  Mean score: {quality_score:.4f} (threshold {min_score})")
    log(f"  Raw scores: {raw_scores}")
    log(f"  RESULT: {verdict}")

    if langfuse_host and langfuse_public and langfuse_secret:
        try:
            write_langfuse_score(
                langfuse_host, langfuse_public, langfuse_secret,
                quality_score, canary_version,
            )
        except Exception as exc:
            log(f"  Warning: Langfuse write failed (non-fatal): {exc}")

    result = {
        "passed": passed,
        "quality_score": quality_score,
        "min_score": min_score,
        "raw_scores": raw_scores,
        "canary_version": canary_version,
    }
    print(json.dumps(result))
    sys.exit(0 if passed else 1)


if __name__ == "__main__":
    main()
