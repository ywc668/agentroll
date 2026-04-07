"""
Unit tests for runner.py — uses an in-process HTTP server to simulate the agent.
Run with: python3 test_runner.py
"""

import http.server
import json
import os
import sys
import threading
import unittest
from unittest.mock import patch

# Bring runner module into scope
sys.path.insert(0, os.path.dirname(__file__))
import runner


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

class MockAgentHandler(http.server.BaseHTTPRequestHandler):
    """Configurable stub agent that returns preset responses."""

    response_data: dict = {}

    def log_message(self, *args):
        pass  # silence server logs during tests

    def do_GET(self):
        if self.path == "/healthz":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok"}).encode())

    def do_POST(self):
        if self.path == "/query":
            length = int(self.headers.get("Content-Length", 0))
            self.rfile.read(length)  # consume body
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(self.response_data).encode())


def make_server(response_data: dict):
    """Start a mock server in a daemon thread; return (server, base_url)."""
    MockAgentHandler.response_data = response_data
    server = http.server.HTTPServer(("127.0.0.1", 0), MockAgentHandler)
    t = threading.Thread(target=server.serve_forever, daemon=True)
    t.start()
    port = server.server_address[1]
    return server, f"http://127.0.0.1:{port}"


# ---------------------------------------------------------------------------
# Tests for check_query
# ---------------------------------------------------------------------------

class TestCheckQuery(unittest.TestCase):

    def _run(self, response_data, *, min_response_len=50, max_latency_ms=10000,
             min_tool_calls=1, content_quality_severity="fail"):
        server, base = make_server(response_data)
        try:
            return runner.check_query(
                f"{base}/query",
                "What pods are running?",
                max_latency_ms,
                min_response_len,
                min_tool_calls,
                content_quality_severity,
            )
        finally:
            server.shutdown()

    # ------------------------------------------------------------------
    # Healthy agent (v1 / production prompt)
    # ------------------------------------------------------------------

    def test_pass_with_good_response(self):
        result = self._run({
            "answer": "All 3 pods in default namespace are running. No issues detected.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            "tool_calls_count": 2,
        })
        self.assertTrue(result["passed"])
        self.assertEqual(result["checks"]["response_length"], True)
        self.assertEqual(result["checks"]["latency"], True)
        self.assertEqual(result["checks"]["content_quality"], True)
        self.assertEqual(result["checks"]["tool_usage"], True)
        self.assertEqual(result["tool_calls_count"], 2)

    # ------------------------------------------------------------------
    # Degraded agent (degraded-v2 prompt) — the core of the bad canary demo
    # ------------------------------------------------------------------

    def test_fail_short_response(self):
        """Response under 50 chars should fail response_length."""
        result = self._run({
            "answer": "Cluster seems fine.",  # 19 chars — clearly under 50
            "prompt_version": "degraded-v2",
            "model_version": "claude-sonnet",
            "tool_calls_count": 0,
        })
        self.assertFalse(result["passed"])
        self.assertEqual(result["checks"]["response_length"], False)

    def test_fail_zero_tool_calls(self):
        """An agent that calls 0 tools should fail tool_usage."""
        result = self._run({
            "answer": "The Kubernetes cluster appears to be operating within normal parameters and no issues have been detected.",
            "prompt_version": "degraded-v2",
            "model_version": "claude-sonnet",
            "tool_calls_count": 0,
        })
        self.assertFalse(result["passed"])
        self.assertEqual(result["checks"]["tool_usage"], False)
        self.assertIn("0 tool calls", result["error"])

    def test_fail_degraded_both_checks(self):
        """Degraded prompt hits both response_length AND tool_usage failures."""
        result = self._run({
            "answer": "Cluster ok.",  # 11 chars
            "prompt_version": "degraded-v2",
            "model_version": "claude-sonnet",
            "tool_calls_count": 0,
        })
        self.assertFalse(result["passed"])
        self.assertEqual(result["checks"]["response_length"], False)
        self.assertEqual(result["checks"]["tool_usage"], False)

    # ------------------------------------------------------------------
    # content_quality check (now a real signal, not overridden)
    # ------------------------------------------------------------------

    def test_fail_content_quality_by_default(self):
        """content_quality_severity=fail should actually fail on error indicators."""
        result = self._run({
            "answer": "Error: cannot connect to Kubernetes API. Failed to list pods in namespace.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            "tool_calls_count": 2,
        }, content_quality_severity="fail")
        self.assertFalse(result["passed"])
        self.assertEqual(result["checks"]["content_quality"], False)

    def test_warn_content_quality(self):
        """content_quality_severity=warn should log but not fail."""
        result = self._run({
            "answer": "Error: cannot connect to Kubernetes API. Failed to list pods in namespace.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            "tool_calls_count": 2,
        }, content_quality_severity="warn")
        # Tool calls and length still pass; content_quality is warn-only
        self.assertEqual(result["checks"]["content_quality"], True)

    # ------------------------------------------------------------------
    # Backward compatibility — old agent without tool_calls_count
    # ------------------------------------------------------------------

    def test_backward_compat_no_tool_calls_count(self):
        """Agents not reporting tool_calls_count (-1 sentinel) should skip tool_usage check."""
        result = self._run({
            "answer": "All deployments are healthy with 3/3 replicas ready. No warning events detected.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            # no tool_calls_count field
        })
        self.assertTrue(result["passed"])
        self.assertNotIn("tool_usage", result["checks"])  # check was skipped
        self.assertEqual(result["tool_calls_count"], -1)  # -1 sentinel: field not in response

    def test_skip_tool_check_when_min_zero(self):
        """MIN_TOOL_CALLS=0 disables the tool_usage check."""
        result = self._run({
            "answer": "Deployments are healthy. All pods running. No events found in cluster.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            "tool_calls_count": 0,
        }, min_tool_calls=0)
        self.assertTrue(result["passed"])
        self.assertNotIn("tool_usage", result["checks"])

    # ------------------------------------------------------------------
    # Latency
    # ------------------------------------------------------------------

    def test_fail_on_slow_response(self):
        """Very tight latency threshold should trigger latency failure."""
        result = self._run({
            "answer": "All pods in default namespace are Running. No issues found.",
            "prompt_version": "v1",
            "model_version": "claude-sonnet",
            "tool_calls_count": 1,
        }, max_latency_ms=0)  # 0ms threshold — always fails since elapsed > 0
        self.assertFalse(result["passed"])
        self.assertEqual(result["checks"]["latency"], False)


# ---------------------------------------------------------------------------
# Tests for check_health
# ---------------------------------------------------------------------------

class TestCheckHealth(unittest.TestCase):

    def test_healthy(self):
        server, base = make_server({})
        try:
            self.assertTrue(runner.check_health(f"{base}/healthz"))
        finally:
            server.shutdown()

    def test_unhealthy_on_connection_error(self):
        self.assertFalse(runner.check_health("http://127.0.0.1:1"))  # nothing listening


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    loader = unittest.TestLoader()
    suite = loader.loadTestsFromModule(sys.modules[__name__])
    runner_obj = unittest.TextTestRunner(verbosity=2)
    result = runner_obj.run(suite)
    sys.exit(0 if result.wasSuccessful() else 1)
