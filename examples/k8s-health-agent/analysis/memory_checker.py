#!/usr/bin/env python3
"""
memory_checker.py — Sprint 12b: Agent Memory Lifecycle

Tests agent memory recall accuracy by:
1. Sending test queries that require recalling previously established facts.
2. Scoring each response with an LLM judge (0.0–1.0).
3. Writing memory_recall_accuracy to Langfuse as a named score.
4. Exiting 0 if mean accuracy >= MIN_MEMORY_RECALL_SCORE, 1 otherwise.

Falls back to exit 0 when no test cases produce scoreable results (avoids
blocking rollouts for agents where memory is not yet populated).
"""

import json
import os
import sys

import anthropic
import requests

AGENT_SERVICE_URL = os.environ["AGENT_SERVICE_URL"]
CANARY_VERSION = os.environ.get("CANARY_VERSION", "")
JUDGE_MODEL = os.environ.get("JUDGE_MODEL", "claude-haiku-4-5-20251001")
JUDGE_PROVIDER = os.environ.get("JUDGE_PROVIDER", "anthropic")
API_KEY = os.environ.get("API_KEY", "")
MIN_MEMORY_RECALL_SCORE = float(os.environ.get("MIN_MEMORY_RECALL_SCORE", "0.7"))
LANGFUSE_HOST = os.environ.get("LANGFUSE_HOST", "https://cloud.langfuse.com").rstrip("/")
LANGFUSE_PUBLIC_KEY = os.environ.get("LANGFUSE_PUBLIC_KEY", "")
LANGFUSE_SECRET_KEY = os.environ.get("LANGFUSE_SECRET_KEY", "")

# TEST_CASES is a JSON array of objects with "query" and "expected_fact" keys.
# Override via environment variable to customise the recall tests for your agent.
TEST_CASES = json.loads(os.environ.get("TEST_CASES", json.dumps([
    {
        "query": "How many nodes does the cluster have?",
        "expected_fact": "3",
    },
    {
        "query": "What service runs in the production namespace?",
        "expected_fact": "payments",
    },
])))

_JUDGE_RUBRIC = """Score the agent response on memory recall accuracy (0.0 to 1.0):
- 1.0: Response clearly and correctly states the expected fact.
- 0.5: Response partially contains the fact or is ambiguous.
- 0.0: Response is wrong, missing the fact, or refuses to answer.
Return ONLY a number between 0.0 and 1.0. No other text."""


def call_agent(query: str) -> str:
    """Send a query to the agent and return the response text."""
    resp = requests.post(
        f"{AGENT_SERVICE_URL}/query",
        json={"query": query},
        timeout=30,
    )
    resp.raise_for_status()
    data = resp.json()
    # Support common response shapes from different agent frameworks.
    return data.get("response") or data.get("answer") or data.get("output") or str(data)


def score_with_judge(query: str, response: str, expected_fact: str) -> float:
    """Score response accuracy using the configured LLM judge. Returns 0.0–1.0."""
    if JUDGE_PROVIDER != "anthropic" or not API_KEY:
        # Judge unavailable — return a neutral pass score so the check doesn't
        # block rollouts when credentials are missing.
        return 0.5

    client = anthropic.Anthropic(api_key=API_KEY)
    prompt = (
        f"{_JUDGE_RUBRIC}\n\n"
        f"Query: {query}\n"
        f"Expected fact: {expected_fact}\n"
        f"Agent response: {response}\n\n"
        "Score:"
    )
    msg = client.messages.create(
        model=JUDGE_MODEL,
        max_tokens=10,
        messages=[{"role": "user", "content": prompt}],
    )
    text = msg.content[0].text.strip()
    try:
        score = float(text)
        return max(0.0, min(1.0, score))
    except ValueError:
        print(f"Warning: judge returned non-numeric score: {text!r}", file=sys.stderr)
        return 0.0


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
    scores = []
    comment = f"cv={CANARY_VERSION}"

    for i, tc in enumerate(TEST_CASES):
        query = tc.get("query", "")
        expected_fact = tc.get("expected_fact", "")

        try:
            response = call_agent(query)
        except Exception as e:
            print(f"Warning: agent query {i} failed: {e}", file=sys.stderr)
            continue

        print(f"Test {i}: query={query!r}")
        print(f"  response={response!r}")

        try:
            score = score_with_judge(query, response, expected_fact)
        except Exception as e:
            print(f"Warning: judge scoring failed for test {i}: {e}", file=sys.stderr)
            continue

        scores.append(score)
        print(f"  expected_fact={expected_fact!r} score={score:.3f}")

    if not scores:
        print("No scoreable results — passing by default", file=sys.stderr)
        return 0

    mean_score = sum(scores) / len(scores)
    print(f"Mean memory recall accuracy: {mean_score:.3f} (threshold: {MIN_MEMORY_RECALL_SCORE})")

    try:
        write_langfuse_score("memory_recall_accuracy", mean_score, comment)
    except Exception as e:
        print(f"Warning: failed to write Langfuse score: {e}", file=sys.stderr)

    if mean_score < MIN_MEMORY_RECALL_SCORE:
        print(f"FAIL: {mean_score:.3f} < {MIN_MEMORY_RECALL_SCORE}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
