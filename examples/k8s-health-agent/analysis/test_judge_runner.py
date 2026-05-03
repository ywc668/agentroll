"""
Unit tests for judge_runner.py — no cluster or real LLM needed.
Run with: python -m pytest test_judge_runner.py -v
  or:     python -m unittest test_judge_runner -v
"""
import json
import unittest
from unittest.mock import patch, MagicMock

import judge_runner


class TestParseJudgeResponse(unittest.TestCase):
    def test_parses_valid_json(self):
        score = judge_runner.parse_judge_score('{"score": 8, "reason": "good"}')
        self.assertEqual(score, 8)

    def test_parses_score_embedded_in_prose(self):
        # LLMs sometimes wrap JSON in prose
        score = judge_runner.parse_judge_score('Here is my score:\n{"score": 6, "reason": "ok"}')
        self.assertEqual(score, 6)

    def test_returns_none_on_garbage(self):
        score = judge_runner.parse_judge_score("I cannot evaluate this.")
        self.assertIsNone(score)

    def test_clamps_score_above_10(self):
        score = judge_runner.parse_judge_score('{"score": 15, "reason": "very good"}')
        self.assertEqual(score, 10)

    def test_clamps_score_below_0(self):
        score = judge_runner.parse_judge_score('{"score": -2, "reason": "bad"}')
        self.assertEqual(score, 0)


class TestComputeMeanScore(unittest.TestCase):
    def test_mean_of_scores(self):
        result = judge_runner.compute_quality_score([8, 6, 7])
        self.assertAlmostEqual(result, 7.0 / 10.0)

    def test_single_score(self):
        result = judge_runner.compute_quality_score([10])
        self.assertAlmostEqual(result, 1.0)

    def test_empty_returns_zero(self):
        result = judge_runner.compute_quality_score([])
        self.assertAlmostEqual(result, 0.0)


class TestBuildLangfusePayload(unittest.TestCase):
    def test_payload_structure(self):
        payload = judge_runner.build_langfuse_score_payload(
            quality_score=0.75,
            composite_version="v1p1.gpt4.abc123",
        )
        self.assertEqual(payload["name"], "judge_quality_score")
        self.assertAlmostEqual(payload["value"], 0.75)
        self.assertIn("cv=v1p1.gpt4.abc123", payload["comment"])
        self.assertEqual(payload["dataType"], "NUMERIC")


if __name__ == "__main__":
    unittest.main()
