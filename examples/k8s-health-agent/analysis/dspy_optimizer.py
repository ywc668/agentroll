#!/usr/bin/env python3
"""
dspy_optimizer.py — Sprint 13.3: DSPy-style Prompt Optimization Job

Reference implementation showing how to use the DSPy library (specifically MIPROv2)
for prompt optimization inside a Kubernetes Job. The controller's Go implementation
performs equivalent logic directly; this script is for teams that want the full
DSPy Python library's MIPRO optimizer.

Required environment variables:
  AGENT_SERVICE_URL    — Base URL of the canary agent (e.g., http://my-agent.default.svc:8080)
  JUDGE_MODEL          — LLM model for scoring (e.g., claude-haiku-4-5-20251001)
  JUDGE_PROVIDER       — LLM provider (anthropic or openai)
  JUDGE_API_KEY        — API key for the judge LLM
  EVAL_HISTORY_JSON    — JSON array of {compositeVersion, qualityScore, verdict} objects
  RESULT_CONFIGMAP     — Name of the ConfigMap to write the optimized prompt to
  NAMESPACE            — Kubernetes namespace
  CURRENT_PROMPT       — (optional) current system prompt text

Output:
  Writes optimized prompt to ConfigMap <RESULT_CONFIGMAP> under key "system_prompt".
  Falls back to anthropic-based LLM optimization if DSPy is not available.
"""

import json
import os
import sys
import textwrap

AGENT_SERVICE_URL = os.environ["AGENT_SERVICE_URL"]
JUDGE_MODEL = os.environ.get("JUDGE_MODEL", "claude-haiku-4-5-20251001")
JUDGE_PROVIDER = os.environ.get("JUDGE_PROVIDER", "anthropic")
JUDGE_API_KEY = os.environ["JUDGE_API_KEY"]
EVAL_HISTORY_JSON = os.environ.get("EVAL_HISTORY_JSON", "[]")
RESULT_CONFIGMAP = os.environ["RESULT_CONFIGMAP"]
NAMESPACE = os.environ["NAMESPACE"]
CURRENT_PROMPT = os.environ.get("CURRENT_PROMPT", "")


def load_eval_history():
    """Parse the eval history JSON passed via environment variable."""
    try:
        return json.loads(EVAL_HISTORY_JSON)
    except json.JSONDecodeError as e:
        print(f"Warning: could not parse EVAL_HISTORY_JSON: {e}", file=sys.stderr)
        return []


def format_training_examples(history):
    """Format eval history as MIPRO-style training examples."""
    lines = []
    for i, entry in enumerate(history, 1):
        version = entry.get("compositeVersion", "unknown")
        score = entry.get("qualityScore", 0.0)
        verdict = entry.get("verdict", "pass" if score >= 0.7 else "fail")
        lines.append(f"Example {i}: version={version} quality={score:.2f} verdict={verdict}")
    return "\n".join(lines)


def optimize_with_dspy(history, current_prompt):
    """
    Run DSPy MIPROv2 optimization. Requires dspy-ai to be installed.
    This is the preferred path for production use.
    """
    import dspy  # noqa: F401 — intentional late import

    class AgentSignature(dspy.Signature):
        """Optimize a system prompt for an AI agent."""
        training_examples: str = dspy.InputField(desc="quality scores from LLM-as-judge evaluations")
        current_prompt: str = dspy.InputField(desc="current system prompt (may be empty)")
        optimized_prompt: str = dspy.OutputField(desc="improved system prompt text only")

    examples_str = format_training_examples(history)
    lm = dspy.LM(f"{JUDGE_PROVIDER}/{JUDGE_MODEL}", api_key=JUDGE_API_KEY)
    dspy.configure(lm=lm)

    optimizer = dspy.MIPROv2(metric=None, auto="light")
    module = dspy.ChainOfThought(AgentSignature)

    # Compile with a minimal teleprompter run — treat eval history as training set.
    trainset = [
        dspy.Example(
            training_examples=examples_str,
            current_prompt=current_prompt,
        ).with_inputs("training_examples", "current_prompt")
    ]
    compiled = optimizer.compile(module, trainset=trainset, num_trials=3)
    result = compiled(training_examples=examples_str, current_prompt=current_prompt)
    return result.optimized_prompt


def optimize_with_llm_fallback(history, current_prompt):
    """
    LLM-based optimization fallback when DSPy is not installed.
    Equivalent to what the Go controller does in-process.
    """
    import urllib.request

    examples_str = format_training_examples(history)
    system_prompt = textwrap.dedent("""
        You are an expert AI agent system prompt optimizer using a DSPy-style approach.
        Analyse the quality pattern in training examples and rewrite the system prompt
        to address weaknesses and reinforce strengths. Output ONLY the new system prompt
        text — no explanation, no markdown, no preamble. Keep under 600 words.
    """).strip()

    user_message = f"""Training examples (composite version → quality score → verdict):
{examples_str}

Current system prompt:
{current_prompt or "(unknown — not configured)"}

Write an improved system prompt. Output ONLY the new prompt text."""

    if JUDGE_PROVIDER == "anthropic":
        url = "https://api.anthropic.com/v1/messages"
        headers = {
            "Content-Type": "application/json",
            "x-api-key": JUDGE_API_KEY,
            "anthropic-version": "2023-06-01",
        }
        payload = json.dumps({
            "model": JUDGE_MODEL,
            "max_tokens": 1024,
            "system": system_prompt,
            "messages": [{"role": "user", "content": user_message}],
        }).encode()
        req = urllib.request.Request(url, data=payload, headers=headers)
        with urllib.request.urlopen(req, timeout=30) as resp:
            result = json.loads(resp.read())
        return result["content"][0]["text"].strip()

    raise ValueError(f"Unsupported provider for fallback: {JUDGE_PROVIDER}")


def write_to_configmap(optimized_prompt):
    """Write the optimized prompt to the result ConfigMap via the K8s API."""
    from kubernetes import client as k8s_client, config as k8s_config

    try:
        k8s_config.load_incluster_config()
    except k8s_config.ConfigException:
        k8s_config.load_kube_config()

    v1 = k8s_client.CoreV1Api()
    body = k8s_client.V1ConfigMap(
        metadata=k8s_client.V1ObjectMeta(name=RESULT_CONFIGMAP, namespace=NAMESPACE),
        data={"system_prompt": optimized_prompt},
    )
    try:
        v1.patch_namespaced_config_map(RESULT_CONFIGMAP, NAMESPACE, body)
    except k8s_client.ApiException as e:
        if e.status == 404:
            v1.create_namespaced_config_map(NAMESPACE, body)
        else:
            raise


def main():
    history = load_eval_history()
    if len(history) < 5:
        print(f"Insufficient training samples ({len(history)} < 5), skipping.", file=sys.stderr)
        sys.exit(0)

    print(f"Optimizing prompt with {len(history)} training examples...", file=sys.stderr)

    optimized = None
    try:
        optimized = optimize_with_dspy(history, CURRENT_PROMPT)
        print("Used DSPy MIPROv2 optimizer.", file=sys.stderr)
    except ImportError:
        print("DSPy not installed, falling back to LLM-based optimization.", file=sys.stderr)
        optimized = optimize_with_llm_fallback(history, CURRENT_PROMPT)
        print("Used LLM fallback optimizer.", file=sys.stderr)

    if not optimized:
        print("Optimizer returned empty prompt, aborting.", file=sys.stderr)
        sys.exit(1)

    write_to_configmap(optimized)
    print(f"Wrote optimized prompt ({len(optimized)} chars) to ConfigMap {RESULT_CONFIGMAP}.", file=sys.stderr)
    sys.exit(0)


if __name__ == "__main__":
    main()
