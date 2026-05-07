"""Unit tests for tool_checker.py — extract_tool_results is pure and easy to test."""
import sys
import os

sys.path.insert(0, os.path.dirname(__file__))

from tool_checker import extract_tool_results


def test_extract_tool_results_success():
    """Two successes and one failure for kubectl-get."""
    observations = [
        {"type": "SPAN", "name": "kubectl-get", "level": "DEFAULT"},
        {"type": "SPAN", "name": "kubectl-get", "level": "DEFAULT"},
        {"type": "SPAN", "name": "kubectl-get", "level": "ERROR"},
    ]
    results = extract_tool_results(observations)
    assert "kubectl-get" in results
    assert results["kubectl-get"]["success"] == 2
    assert results["kubectl-get"]["fail"] == 1


def test_extract_tool_results_empty():
    """Empty observation list returns empty dict."""
    assert extract_tool_results([]) == {}


def test_extract_tool_results_non_tool_skipped():
    """GENERATION-type observations are not tool calls and are skipped."""
    observations = [
        {"type": "GENERATION", "name": "claude-3-5-sonnet", "level": "DEFAULT"},
        {"type": "SPAN", "name": "kubectl-get", "level": "DEFAULT"},
    ]
    results = extract_tool_results(observations)
    assert "claude-3-5-sonnet" not in results
    assert results["kubectl-get"]["success"] == 1
    assert results["kubectl-get"]["fail"] == 0
