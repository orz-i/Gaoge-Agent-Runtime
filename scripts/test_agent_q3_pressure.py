"""Q3-A: offline-only attribution of validated Agent quality reports.

This suite makes no Provider calls and never reads operator-owned evidence.
"""
import contextlib
import copy
import io
import unittest
from unittest import mock

import test_agent_baseline as fixtures
import test_agent_quality as quality

baseline = fixtures.baseline


def failed_quality_report():
    result = quality.quality_report(failed_case="distractor-05")
    sample = next(item for item in result["samples"] if item["caseID"] == "distractor-05")
    sample.update(stage="evidence", failureCode="context_budget_exceeded", artifactReads=2,
                  draftWrites=0)
    sample["toolExecutions"][2]["toolKey"] = "fixture.read"
    # Preserve valid registered Tool totals without claiming a draft receipt.
    # Query metadata is absent here, as in the original historical quality observation.
    return result


class QualityContextPressureTests(unittest.TestCase):
    def test_historical_context_rejection_reports_only_observed_evidence(self):
        report = failed_quality_report()
        original = copy.deepcopy(report)
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(report, original)
        self.assertEqual(facts["counts"]["contextBudgetRejected"], 1)
        self.assertEqual(facts["counts"]["agentToolLimit"], 0)
        self.assertEqual(facts["countCoverage"]["samplesWithCount"], 0)
        self.assertEqual(facts["coverage"]["attempted"], 40)
        rejected = facts["failures"][0]
        self.assertEqual(rejected["caseID"], "distractor-05")
        self.assertEqual(rejected["category"], "local_context_budget_rejection")
        self.assertEqual(rejected["artifactReads"], 2)
        self.assertIsNone(rejected["nativeCount"])
        self.assertIsNone(rejected["artifactReadModes"])
        self.assertIsNone(rejected["rejectedRequestAssessment"])
        self.assertIsNone(rejected["compactionOutcome"])
        rendered = baseline.quality_pressure_markdown(facts)
        self.assertIn("context_budget_exceeded", rendered)
        self.assertIn("未记录", rendered)
        self.assertIn("Context 拒绝不是 Provider HTTP 4xx 拒绝", rendered)
        self.assertNotIn("计数失败导致", rendered)

    def test_trusted_tool_limit_is_not_a_context_failure(self):
        report = failed_quality_report()
        sample = next(item for item in report["samples"] if item["caseID"] == "distractor-05")
        sample["failureCode"] = "task_failed_before_interruption"
        sample["preInterruption"] = {"turnStatus": "failed", "agentErrorCode": "agent.tool_limit"}
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(facts["counts"]["agentToolLimit"], 1)
        self.assertEqual(facts["counts"]["contextBudgetRejected"], 0)
        self.assertEqual(facts["failures"][0]["category"], "trusted_agent_tool_limit")
        report["samples"][20].pop("preInterruption")
        self.assertEqual(baseline.quality_pressure_facts(report)["failures"][0]["category"], "other_or_unknown")

    def test_real_rejected_request_assessment_is_reported_separately_from_aggregate_count(self):
        report = failed_quality_report()
        sample = next(s for s in report["samples"] if s["caseID"] == "distractor-05")
        report["runner"]["profile"]["contextPolicy"]["maxSerializedBytes"] = 4 << 20
        sample["contextPressure"] = {
            "countSource": "estimated", "hardInputTokens": 8192,
            "softInputTokens": 6553, "hardTokenEstimate": 14100,
            "serializedBytes": 12000, "maxSerializedBytes": 4 << 20,
            "reason": "no_safe_compaction",
        }
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(facts["failures"][0]["rejectedRequestAssessment"]["hardTokenEstimate"], 14100)
        self.assertEqual(facts["failures"][0]["compactionOutcome"], "no_safe_compaction")
        text = baseline.quality_pressure_markdown(facts)
        self.assertIn("观测 1 / 1", text)
        self.assertIn("Soft=6553", text)
        self.assertIn("14100", text)
        # An exact count can fit the Token budget while the separate
        # serialized-byte limit still denies the request.
        bytes_only = copy.deepcopy(report)
        bytes_only_pressure = next(s for s in bytes_only["samples"]
                                   if s["caseID"] == "distractor-05")["contextPressure"]
        bytes_only_pressure.update(countSource="exact", hardTokenEstimate=1024,
                                   serializedBytes=(4 << 20) + 1,
                                   reason="no_artifact_reader")
        bytes_facts = baseline.quality_pressure_facts(bytes_only)
        self.assertEqual(bytes_facts["failures"][0]["rejectedRequestAssessment"]["hardTokenEstimate"], 1024)
        self.assertEqual(bytes_facts["failures"][0]["compactionOutcome"], "no_artifact_reader")
        for change in (
            lambda s: s["contextPressure"].update(countSource="forged"),
            lambda s: s["contextPressure"].update(hardInputTokens=1),
            lambda s: s["contextPressure"].update(hardTokenEstimate=1),
            lambda s: s["contextPressure"].update(maxSerializedBytes=100),
            lambda s: s["contextPressure"].update(reason="secret response"),
            lambda s: s["contextPressure"].update(serializedBytes=True),
            lambda s: s["contextPressure"].update(secret="payload"),
            lambda s: s.update(failureCode="oracle_failed"),
        ):
            candidate = copy.deepcopy(report)
            wrong = next(s for s in candidate["samples"] if s["caseID"] == "distractor-05")
            change(wrong)
            with self.subTest(change=change), self.assertRaises(baseline.SafeFailure):
                baseline.quality_pressure_facts(candidate)

    def test_aggregate_counter_is_not_a_per_rejected_request_proof(self):
        report = failed_quality_report()
        sample = next(item for item in report["samples"] if item["caseID"] == "distractor-05")
        sample["inputCountObservations"] = {"attempts": 5, "accepted": 4, "unavailable": 1,
            "unavailableReasons": {"localRejected": 1, "http4xx": 0, "http5xx": 0,
                                   "otherHTTP": 0, "transportOrProtocol": 0, "other": 0}}
        sample["inputCountComparison"] = {"compared": 0, "equal": 0, "belowUsage": 0,
                                          "aboveUsage": 0, "maxUnderestimate": 0}
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(facts["countCoverage"]["attempts"], 5)
        self.assertEqual(facts["countCoverage"]["samplesWithCount"], 1)
        failure = facts["failures"][0]
        self.assertEqual(failure["nativeCount"], {"attempts": 5, "accepted": 4,
                                                   "unavailable": 1, "matchedUsage": 0})
        self.assertIsNone(failure["rejectedRequestAssessment"])
        self.assertIsNone(failure["compactionOutcome"])
        text = baseline.quality_pressure_markdown(facts)
        self.assertIn("5 / 4 / 1", text)
        self.assertIn("不能定位到最终被拒绝的请求", text)

    def test_matched_input_undercount_is_a_separate_safety_signal(self):
        config = quality.quality_config()
        report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
        baseline.accept_event(report, quality.quality_start(config))
        baseline.accept_event(report, {"type": "preflight", "status": "passed",
                                       "durationMs": 1, "usage": None})
        sample = quality.quality_samples(config, failed_case="distractor-05")[20]
        sample["usage"] = {"knownCalls": 1, "missingCalls": 3, "complete": False,
                           "known": {"inputTokens": 100, "outputTokens": 20,
                                     "cacheReadTokens": 0, "cacheWriteTokens": 0, "reasoningTokens": 0}}
        sample["inputCountObservations"] = {"attempts": 1, "accepted": 1, "unavailable": 0}
        sample["inputCountComparison"] = {"compared": 1, "equal": 0, "belowUsage": 1,
                                          "aboveUsage": 0, "maxUnderestimate": 7}
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(facts["countCoverage"]["belowUsage"], 1)
        text = baseline.quality_pressure_markdown(facts)
        self.assertIn("独立的计量安全复核信号", text)
        self.assertIn("其他或根因未知 1", text)
        self.assertNotIn("计数低估导致", text)

    def test_conflicting_trusted_tool_terminal_and_context_code_stays_unknown(self):
        report = failed_quality_report()
        sample = next(item for item in report["samples"] if item["caseID"] == "distractor-05")
        sample["preInterruption"] = {"turnStatus": "failed", "agentErrorCode": "agent.tool_limit"}
        facts = baseline.quality_pressure_facts(report)
        self.assertEqual(facts["counts"], {"contextBudgetRejected": 0,
                                           "agentToolLimit": 0, "otherOrUnknown": 1})
        self.assertEqual(facts["failures"][0]["category"], "other_or_unknown")

    def test_only_allowlisted_facts_leave_private_reports(self):
        report = failed_quality_report()
        report["samples"][20]["unrecognizedPrivateTranscript"] = "DO_NOT_ECHO_PRIVATE_PROVIDER_PAYLOAD"
        facts = baseline.quality_pressure_facts(report)
        text = baseline.quality_pressure_markdown(facts)
        self.assertNotIn("DO_NOT_ECHO_PRIVATE_PROVIDER_PAYLOAD", text)
        self.assertNotIn("unrecognizedPrivateTranscript", str(facts))
        self.assertEqual(facts["failures"][0]["caseID"], "distractor-05")

    def test_unknown_extension_codes_and_nonhash_metadata_are_never_echoed(self):
        report = failed_quality_report()
        sample = next(item for item in report["samples"] if item["caseID"] == "distractor-05")
        sample["failureCode"] = "sk-CONFIDENTIAL-CALLER-METADATA"
        report["runner"]["datasetHash"] = "CONFIDENTIAL-IN-HASH-FIELD"
        facts = baseline.quality_pressure_facts(report)
        self.assertIsNone(facts["datasetHash"])
        self.assertEqual(facts["failures"][0]["failureCode"], "unrecognized_code")
        rendered = baseline.quality_pressure_markdown(facts)
        self.assertNotIn("CONFIDENTIAL", rendered)
        self.assertNotIn("sk-", rendered)

    def test_pilot_and_partial_report_never_claim_complete_quality_evidence(self):
        pilot_config = quality.quality_config()
        report = baseline.new_report(pilot_config, {"revision": "a" * 40, "dirty": False})
        start = quality.quality_start(pilot_config)
        start.update(pilotOnly=True, pilotCaseID="distractor-05")
        baseline.accept_event(report, start)
        baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 1, "usage": None})
        sample = quality.quality_samples(pilot_config, failed_case="distractor-05")[20]
        sample["stage"] = "evidence"
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        # An interrupted / absent summary is not a full forty-case report.
        facts = baseline.quality_pressure_facts(report)
        self.assertTrue(facts["coverage"]["pilotOnly"])
        self.assertFalse(facts["coverage"]["complete"])
        self.assertEqual(facts["coverage"]["attempted"], 1)
        self.assertEqual(facts["coverage"]["requested"], 40)
        self.assertIn("不是完整质量得分", baseline.quality_pressure_markdown(facts))

    def test_fail_closed_invalid_reports_legacy_suite_and_untrusted_fields(self):
        with self.assertRaises(baseline.SafeFailure):
            baseline.quality_pressure_facts(fixtures.complete_report())
        for malformed in (
            lambda r: r["samples"][0].update(caseID="private-api-key"),
            lambda r: r["samples"][0].update(inputCountObservations={"attempts": 3, "accepted": 3, "unavailable": 2}),
            lambda r: r["samples"][0].update(lastHTTPStatus=400),
            lambda r: r["samples"][0].update(preInterruption={"turnStatus": "failed", "agentErrorCode": "private-secret"}),
        ):
            report = failed_quality_report()
            malformed(report)
            with self.subTest(malformed=malformed), self.assertRaises(baseline.SafeFailure):
                baseline.quality_pressure_facts(report)

    def test_readonly_cli_never_launches_provider_or_rewrites_report(self):
        report = failed_quality_report()
        original = copy.deepcopy(report)
        with mock.patch.object(baseline, "read_report", return_value=report), \
             mock.patch.object(baseline, "launch", side_effect=AssertionError("must not launch")), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(baseline.main(["diagnose-quality", "history.json"]), 0)
        self.assertIn("Context 压力归因", output.getvalue())
        self.assertEqual(original, report)
        with mock.patch.object(baseline, "read_report", return_value=fixtures.complete_report()), \
             mock.patch.object(baseline, "launch", side_effect=AssertionError("must not launch")), \
             contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(baseline.main(["diagnose-quality", "historical-two-case.json"]), 2)


if __name__ == "__main__":
    unittest.main()
