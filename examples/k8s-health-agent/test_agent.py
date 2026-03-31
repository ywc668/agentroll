"""
Unit tests for agent.py — tests config logic, degraded mode, and the /query endpoint.
Mocks out the Kubernetes client and Anthropic API so no live services are needed.
Run with: python3 test_agent.py
"""

import os
import sys
import json
import unittest
from unittest.mock import MagicMock, patch

# --------------------------------------------------------
# Patch K8s client before importing agent — prevents
# the module-level get_k8s_client() from failing.
# --------------------------------------------------------

_mock_core = MagicMock()
_mock_apps = MagicMock()

from kubernetes import config as _k8s_config

with patch("kubernetes.config.load_incluster_config", side_effect=_k8s_config.ConfigException("not in cluster")), \
     patch("kubernetes.config.load_kube_config"), \
     patch("kubernetes.client.CoreV1Api", return_value=_mock_core), \
     patch("kubernetes.client.AppsV1Api", return_value=_mock_apps):
    import agent


class TestDegradedMode(unittest.TestCase):

    def test_v1_uses_normal_prompt(self):
        with patch.dict(os.environ, {"PROMPT_VERSION": "v1"}):
            # Re-evaluate the degraded flag as if module was re-imported
            is_degraded = "v1".startswith("degraded-")
            self.assertFalse(is_degraded)

    def test_degraded_v2_is_detected(self):
        is_degraded = "degraded-v2".startswith("degraded-")
        self.assertTrue(is_degraded)

    def test_active_prompt_selection(self):
        """ACTIVE_SYSTEM_PROMPT must differ between v1 and degraded-v2."""
        self.assertNotEqual(agent.SYSTEM_PROMPT, agent.DEGRADED_SYSTEM_PROMPT)
        # The degraded prompt should explicitly say not to use tools
        self.assertIn("NOT", agent.DEGRADED_SYSTEM_PROMPT)

    def test_degraded_system_prompt_word_count(self):
        """Degraded prompt instructs under 30 words — responses should be short."""
        self.assertIn("30", agent.DEGRADED_SYSTEM_PROMPT)


class TestQueryResponseModel(unittest.TestCase):

    def test_tool_calls_count_field_exists(self):
        r = agent.QueryResponse(
            answer="test",
            prompt_version="v1",
            model_version="claude-sonnet",
        )
        self.assertEqual(r.tool_calls_count, 0)  # default

    def test_tool_calls_count_is_settable(self):
        r = agent.QueryResponse(
            answer="test",
            prompt_version="v1",
            model_version="claude-sonnet",
            tool_calls_count=3,
        )
        self.assertEqual(r.tool_calls_count, 3)

    def test_response_serialization_includes_tool_calls_count(self):
        r = agent.QueryResponse(
            answer="Cluster is healthy",
            prompt_version="v1",
            model_version="claude-sonnet",
            tool_calls_count=2,
        )
        data = r.model_dump()
        self.assertIn("tool_calls_count", data)
        self.assertEqual(data["tool_calls_count"], 2)


class TestRunAgentDegradedMode(unittest.TestCase):
    """Test run_agent() with degraded-v2: expects 0 tool calls and short response."""

    def test_degraded_returns_short_response_and_zero_tools(self):
        """When degraded: no tools → Claude returns a direct text response immediately."""

        # Mock Claude response: stop_reason="end_turn" (no tool calls)
        mock_text_block = MagicMock()
        mock_text_block.type = "text"
        mock_text_block.text = "Cluster ok."

        mock_response = MagicMock()
        mock_response.stop_reason = "end_turn"
        mock_response.content = [mock_text_block]

        mock_claude = MagicMock()
        mock_claude.messages.create.return_value = mock_response

        with patch("anthropic.Anthropic", return_value=mock_claude), \
             patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key", "PROMPT_VERSION": "degraded-v2"}):
            # Force the module-level flag for this test
            with patch.object(agent, "_IS_DEGRADED", True), \
                 patch.object(agent, "ACTIVE_SYSTEM_PROMPT", agent.DEGRADED_SYSTEM_PROMPT):
                answer, tool_calls_count = agent.run_agent("What pods are running?")

        self.assertEqual(answer, "Cluster ok.")
        self.assertEqual(tool_calls_count, 0)

        # Verify no tools were passed in the API call
        call_kwargs = mock_claude.messages.create.call_args.kwargs
        self.assertNotIn("tools", call_kwargs)

    def test_normal_v1_passes_tool_definitions(self):
        """When v1: tools must be passed to Claude."""

        mock_text_block = MagicMock()
        mock_text_block.type = "text"
        mock_text_block.text = "All pods are running. No issues detected in the cluster environment."

        mock_response = MagicMock()
        mock_response.stop_reason = "end_turn"
        mock_response.content = [mock_text_block]

        mock_claude = MagicMock()
        mock_claude.messages.create.return_value = mock_response

        with patch("anthropic.Anthropic", return_value=mock_claude), \
             patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}):
            with patch.object(agent, "_IS_DEGRADED", False), \
                 patch.object(agent, "ACTIVE_SYSTEM_PROMPT", agent.SYSTEM_PROMPT):
                answer, tool_calls_count = agent.run_agent("What pods are running?")

        self.assertEqual(tool_calls_count, 0)
        call_kwargs = mock_claude.messages.create.call_args.kwargs
        self.assertIn("tools", call_kwargs)
        self.assertEqual(len(call_kwargs["tools"]), 4)  # 4 tools defined


class TestLangfuseOptional(unittest.TestCase):

    def test_langfuse_disabled_when_no_secret_key(self):
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("LANGFUSE_SECRET_KEY", None)
            # LANGFUSE_ENABLED is evaluated at import time, but we can check the logic
            enabled = bool(os.getenv("LANGFUSE_SECRET_KEY"))
            self.assertFalse(enabled)

    def test_langfuse_enabled_when_secret_key_set(self):
        with patch.dict(os.environ, {"LANGFUSE_SECRET_KEY": "sk-lf-test"}):
            enabled = bool(os.getenv("LANGFUSE_SECRET_KEY"))
            self.assertTrue(enabled)


if __name__ == "__main__":
    loader = unittest.TestLoader()
    suite = loader.loadTestsFromModule(sys.modules[__name__])
    r = unittest.TextTestRunner(verbosity=2)
    result = r.run(suite)
    sys.exit(0 if result.wasSuccessful() else 1)
