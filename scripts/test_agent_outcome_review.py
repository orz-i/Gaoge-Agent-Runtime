"""Offline task-outcome review: no paid calls, raw payloads or score inflation."""

import contextlib
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import test_agent_baseline as fixtures
import test_agent_pilot as pilot
import test_agent_quality as quality

baseline = fixtures.baseline


class TaskOutcomeReviewTests(unittest.TestCase):
    def test_complete_report_separates_task_oracle_and_recovery(self):
        report = quality.quality_report(failed_case="unicode-03")
        facts = baseline.quality_outcome_facts(report)
        self.assertEqual(facts["passed"], 39)
        self.assertEqual(facts["failed"], 1)
        self.assertEqual(facts["oraclePassed"], 39)
        self.assertEqual(facts["recoveryProven"], 39)
        self.assertEqual(facts["fullSuccessRate"], 39 / 40)
        self.assertTrue(facts["baselineReviewReady"])  # complete != all passed
        self.assertIsNone(facts["knownTokens"])
        text = baseline.quality_outcome_markdown(facts)
        self.assertIn("97.5%", text)
        self.assertIn("费用未记录", text)
        self.assertIn("不是代码修改", text)
        self.assertIn("不等于恢复执行失败", text)
        self.assertEqual(facts["modelCalls"], 160)
        self.assertEqual(facts["toolCalls"], 120)

    def test_pilot_never_becomes_full_score_or_baseline(self):
        report = pilot.pilot_report()
        facts = baseline.quality_outcome_facts(report)
        self.assertEqual(facts["passed"], 1)
        self.assertEqual(facts["coverage"]["requested"], 40)
        self.assertIsNone(facts["fullSuccessRate"])
        self.assertFalse(facts["baselineReviewReady"])
        text = baseline.quality_outcome_markdown(facts)
        self.assertNotIn("100.0%", text)
        self.assertIn("不是完整质量得分", text)

    def test_known_tokens_require_usage_for_every_model_call(self):
        report = quality.quality_report()
        known = {"inputTokens": 10, "outputTokens": 5, "cacheReadTokens": 0,
                 "cacheWriteTokens": 0, "reasoningTokens": 0}
        for sample in report["samples"]:
            sample["usage"] = {"knownCalls": 4, "missingCalls": 0, "complete": True, "known": dict(known)}
        report["summary"]["usage"] = {
            "knownCalls": 160, "missingCalls": 0, "complete": True,
            "known": {key: value * 40 for key, value in known.items()},
        }
        self.assertEqual(baseline.quality_outcome_facts(report)["knownTokens"], 600)
        report["samples"][-1]["usage"] = fixtures.usage()
        report["summary"]["usage"] = {
            "knownCalls": 156, "missingCalls": 4, "complete": False,
            "known": {key: value * 39 for key, value in known.items()},
        }
        self.assertIsNone(baseline.quality_outcome_facts(report)["knownTokens"])

    def test_oracle_failure_does_not_erase_successful_recovery_evidence(self):
        report = quality.quality_report(failed_case="unicode-03")
        failed = next(sample for sample in report["samples"] if sample["status"] == "failed")
        failed["recoveryChecks"] = dict.fromkeys(baseline.RECOVERY_FIELDS, True)
        facts = baseline.quality_outcome_facts(report)
        self.assertEqual(facts["passed"], 39)
        self.assertEqual(facts["oraclePassed"], 39)
        self.assertEqual(facts["recoveryProven"], 40)

    def test_partial_report_has_unknown_unrun_outcomes(self):
        config = quality.quality_config()
        report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
        baseline.accept_event(report, quality.quality_start(config))
        baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 1, "usage": None})
        baseline.accept_event(report, {"type": "sample", "sample": quality.quality_samples(config)[0]})
        facts = baseline.quality_outcome_facts(report)
        self.assertIsNone(facts["fullSuccessRate"])
        self.assertEqual(facts["failed"], 0)  # unrun tasks are not failed attempts
        self.assertEqual(sum(group["planned"] for group in facts["groups"]), 40)
        self.assertEqual(sum(group["attempted"] for group in facts["groups"]), 1)

    def test_no_samples_does_not_invent_latency_or_tokens(self):
        report = baseline.new_report(quality.quality_config(), {})
        facts = baseline.quality_outcome_facts(report)
        self.assertIsNone(facts["p50Ms"])
        self.assertIsNone(facts["p95Ms"])
        self.assertIsNone(facts["knownTokens"])
        self.assertFalse(facts["baselineReviewReady"])
        self.assertEqual(facts["coverage"]["attempted"], 0)

    def test_dirty_and_unknown_source_stay_ineligible(self):
        for source in ({"revision": "a" * 40, "dirty": True},
                       {"revision": "not-a-revision", "dirty": False}, {}):
            report = quality.quality_report()
            report["source"] = source
            facts = baseline.quality_outcome_facts(report)
            self.assertFalse(facts["baselineReviewReady"])
            self.assertTrue(facts["evidenceGaps"])

    def test_checkpoint_without_start_ignores_unvalidated_runner_metadata(self):
        report = baseline.new_report(quality.quality_config(), {})
        report["runner"].update(requestedSamples=["private-checkpoint-text"],
                                datasetHash="sha256:" + "a" * 64, pilotOnly=True)
        pressure = baseline.quality_pressure_facts(report)
        self.assertIsNone(pressure["datasetHash"])
        self.assertIsNone(pressure["coverage"]["requested"])
        self.assertFalse(pressure["coverage"]["pilotOnly"])
        review = baseline.quality_outcome_facts(report)
        self.assertIsNone(review["coverage"]["requested"])
        self.assertNotIn("private-checkpoint-text", baseline.quality_outcome_markdown(review))
        self.assertNotIn("private-checkpoint-text", baseline.quality_pressure_markdown(pressure))

    def test_review_output_is_content_free(self):
        report = quality.quality_report()
        secret = "private-untrusted-endpoint-or-payload"
        report["source"]["revision"] = secret
        report["extra"] = {"rawResponse": secret}
        report["samples"][0]["unrecognizedRawPayload"] = secret
        # Preserve the existing protocol's extension handling, but never render
        # an unknown field or treat it as evidence.
        text = baseline.quality_outcome_markdown(baseline.quality_outcome_facts(report))
        self.assertNotIn(secret, text)
        self.assertNotIn(report["configuration"]["endpoint"], text)
        self.assertNotIn("fixture-run", text)

    def test_legacy_and_forged_complete_reports_are_rejected(self):
        with self.assertRaises(baseline.SafeFailure):
            baseline.quality_outcome_facts(fixtures.complete_report())
        forged = pilot.pilot_report()
        forged["summary"]["complete"] = True
        with self.assertRaises(baseline.SafeFailure):
            baseline.quality_outcome_facts(forged)

    def test_review_is_read_only_and_never_launches(self):
        report = quality.quality_report()
        before = copy.deepcopy(report)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "original.json"
            raw = json.dumps(report).encode("utf-8")
            path.write_bytes(raw)
            out = io.StringIO()
            with mock.patch.object(baseline, "launch", side_effect=AssertionError("paid call")), \
                    contextlib.redirect_stdout(out):
                self.assertEqual(baseline.main(["review-quality", str(path)]), 0)
            self.assertEqual(path.read_bytes(), raw)
        self.assertEqual(report, before)
        self.assertIn("任务效果复盘", out.getvalue())


if __name__ == "__main__":
    unittest.main()
