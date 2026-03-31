"""
Unit tests for langfuse_metrics.py — mocks Langfuse HTTP API responses.
Run with: python3 test_langfuse_metrics.py
"""

import http.server
import json
import os
import sys
import threading
import unittest
from urllib.parse import urlparse, parse_qs

sys.path.insert(0, os.path.dirname(__file__))
import langfuse_metrics as lm


# ---------------------------------------------------------------------------
# Mock Langfuse API server
# ---------------------------------------------------------------------------

class MockLangfuseHandler(http.server.BaseHTTPRequestHandler):
    """Configurable stub Langfuse server."""

    # Class-level state set by each test
    traces_by_page: dict = {1: []}   # page -> list of trace objects
    observations_by_trace: dict = {}  # trace_id -> list of observation objects
    auth_error: bool = False

    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.auth_error:
            self.send_response(401)
            self.end_headers()
            return

        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)

        if parsed.path == "/api/public/traces":
            page = int(params.get("page", ["1"])[0])
            traces = self.traces_by_page.get(page, [])
            total_pages = max(self.traces_by_page.keys()) if self.traces_by_page else 1
            payload = {
                "data": traces,
                "meta": {"page": page, "limit": 100, "totalPages": total_pages, "totalItems": sum(len(v) for v in self.traces_by_page.values())},
            }
            self._json(200, payload)

        elif parsed.path.startswith("/api/public/observations"):
            trace_id = params.get("traceId", [""])[0]
            obs = self.observations_by_trace.get(trace_id, [])
            self._json(200, {"data": obs})

    def _json(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def make_langfuse_server(traces_by_page=None, observations_by_trace=None, auth_error=False):
    MockLangfuseHandler.traces_by_page = traces_by_page or {1: []}
    MockLangfuseHandler.observations_by_trace = observations_by_trace or {}
    MockLangfuseHandler.auth_error = auth_error
    server = http.server.HTTPServer(("127.0.0.1", 0), MockLangfuseHandler)
    t = threading.Thread(target=server.serve_forever, daemon=True)
    t.start()
    port = server.server_address[1]
    return server, f"http://127.0.0.1:{port}"


def make_trace(trace_id: str) -> dict:
    return {"id": trace_id, "name": "run_agent", "tags": [f"canary:v2.sonnet.1.2.3"]}


def make_tool_obs(obs_id: str, trace_id: str, level: str = "DEFAULT") -> dict:
    return {
        "id": obs_id,
        "traceId": trace_id,
        "type": "SPAN",
        "name": "tool_call_list_pods",
        "level": level,
    }


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

class TestMakeAuthHeader(unittest.TestCase):
    def test_basic_auth_format(self):
        import base64
        header = lm.make_auth_header("pk-test", "sk-test")
        self.assertTrue(header.startswith("Basic "))
        decoded = base64.b64decode(header[6:]).decode()
        self.assertEqual(decoded, "pk-test:sk-test")


class TestComputeToolSuccessRate(unittest.TestCase):

    def test_all_successful(self):
        server, host = make_langfuse_server(
            traces_by_page={1: [make_trace("t1"), make_trace("t2")]},
            observations_by_trace={
                "t1": [make_tool_obs("o1", "t1"), make_tool_obs("o2", "t1")],
                "t2": [make_tool_obs("o3", "t2")],
            },
        )
        try:
            auth = lm.make_auth_header("pk", "sk")
            traces = [make_trace("t1"), make_trace("t2")]
            result = lm.compute_tool_success_rate(host, auth, traces)
            self.assertEqual(result["total_tool_calls"], 3)
            self.assertEqual(result["successful_tool_calls"], 3)
            self.assertAlmostEqual(result["success_rate"], 1.0)
        finally:
            server.shutdown()

    def test_partial_failures(self):
        server, host = make_langfuse_server(
            traces_by_page={1: [make_trace("t1")]},
            observations_by_trace={
                "t1": [
                    make_tool_obs("o1", "t1", level="DEFAULT"),  # success
                    make_tool_obs("o2", "t1", level="ERROR"),    # failure
                    make_tool_obs("o3", "t1", level="DEFAULT"),  # success
                ],
            },
        )
        try:
            auth = lm.make_auth_header("pk", "sk")
            traces = [make_trace("t1")]
            result = lm.compute_tool_success_rate(host, auth, traces)
            self.assertEqual(result["total_tool_calls"], 3)
            self.assertEqual(result["successful_tool_calls"], 2)
            self.assertAlmostEqual(result["success_rate"], round(2/3, 4))
        finally:
            server.shutdown()

    def test_no_tool_spans_returns_zero_rate(self):
        """Traces with no tool_call spans → rate 0.0, inconclusive."""
        server, host = make_langfuse_server(
            traces_by_page={1: [make_trace("t1")]},
            observations_by_trace={
                "t1": [
                    # Only a GENERATION span, not a tool call
                    {"id": "o1", "traceId": "t1", "type": "GENERATION", "name": "llm_call", "level": "DEFAULT"},
                ],
            },
        )
        try:
            auth = lm.make_auth_header("pk", "sk")
            traces = [make_trace("t1")]
            result = lm.compute_tool_success_rate(host, auth, traces)
            self.assertEqual(result["total_tool_calls"], 0)
            self.assertAlmostEqual(result["success_rate"], 0.0)
        finally:
            server.shutdown()


class TestCollectAllTraces(unittest.TestCase):

    def test_single_page(self):
        traces = [make_trace(f"t{i}") for i in range(3)]
        server, host = make_langfuse_server(traces_by_page={1: traces})
        try:
            auth = lm.make_auth_header("pk", "sk")
            result = lm.collect_all_traces(host, auth, "v2.sonnet.1.2.3", "2026-01-01T00:00:00Z")
            self.assertEqual(len(result), 3)
        finally:
            server.shutdown()

    def test_multi_page(self):
        page1 = [make_trace(f"t{i}") for i in range(3)]
        page2 = [make_trace(f"t{i}") for i in range(3, 5)]
        server, host = make_langfuse_server(traces_by_page={1: page1, 2: page2})
        try:
            auth = lm.make_auth_header("pk", "sk")
            result = lm.collect_all_traces(host, auth, "v2.sonnet.1.2.3", "2026-01-01T00:00:00Z")
            self.assertEqual(len(result), 5)
        finally:
            server.shutdown()

    def test_empty_traces(self):
        server, host = make_langfuse_server(traces_by_page={1: []})
        try:
            auth = lm.make_auth_header("pk", "sk")
            result = lm.collect_all_traces(host, auth, "v2.sonnet.1.2.3", "2026-01-01T00:00:00Z")
            self.assertEqual(result, [])
        finally:
            server.shutdown()


class TestMainInconclusive(unittest.TestCase):
    """main() with fewer traces than MIN_TRACES should pass with inconclusive flag."""

    def test_insufficient_traces_passes(self):
        # Only 2 traces, MIN_TRACES=5 → inconclusive pass
        traces = [make_trace("t1"), make_trace("t2")]
        server, host = make_langfuse_server(traces_by_page={1: traces})
        try:
            env = {
                "LANGFUSE_HOST": host,
                "LANGFUSE_PUBLIC_KEY": "pk",
                "LANGFUSE_SECRET_KEY": "sk",
                "CANARY_VERSION": "v2.sonnet.1.2.3",
                "MIN_TRACES": "5",
                "TIME_WINDOW_MINUTES": "10",
                "MIN_SUCCESS_RATE": "0.90",
            }
            with unittest.mock.patch.dict(os.environ, env):
                with self.assertRaises(SystemExit) as cm:
                    lm.main()
            self.assertEqual(cm.exception.code, 0)  # inconclusive → pass
        finally:
            server.shutdown()

    def test_missing_config_fails(self):
        env = {
            "LANGFUSE_HOST": "",
            "LANGFUSE_PUBLIC_KEY": "",
            "LANGFUSE_SECRET_KEY": "",
            "CANARY_VERSION": "",
        }
        with unittest.mock.patch.dict(os.environ, env, clear=False):
            # Clear any existing values
            for k in env:
                os.environ.pop(k, None)
            with self.assertRaises(SystemExit) as cm:
                lm.main()
        self.assertEqual(cm.exception.code, 1)


# Need unittest.mock for patch.dict
import unittest.mock


if __name__ == "__main__":
    loader = unittest.TestLoader()
    suite = loader.loadTestsFromModule(sys.modules[__name__])
    r = unittest.TextTestRunner(verbosity=2)
    result = r.run(suite)
    sys.exit(0 if result.wasSuccessful() else 1)
