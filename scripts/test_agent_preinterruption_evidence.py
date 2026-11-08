"""No raw Provider error or untrusted Store ErrorCode in Agent reports."""
import copy
import unittest

import test_agent_baseline as fixtures
import test_agent_quality as quality

baseline = fixtures.baseline


def report_with_sample(evidence):
    config = quality.quality_config()
    report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
    baseline.accept_event(report, quality.quality_start(config))
    baseline.accept_event(report, {"type":"preflight","status":"passed","durationMs":1,"usage":None})
    sample = quality.quality_samples(config, failed_case="distractor-05")[20]
    assert sample["caseID"] == "distractor-05"
    sample["stage"] = "evidence"
    if evidence is not None:
        sample["preInterruption"] = copy.deepcopy(evidence)
    return report, sample


class PreInterruptionTurnEvidenceTests(unittest.TestCase):
    def test_fixed_agent_failure_is_visible_without_inferring_provider_cause(self):
        report, sample = report_with_sample({"turnStatus":"failed","agentErrorCode":"agent.tool_invalid"})
        baseline.accept_event(report, {"type":"sample","sample":sample})
        lines = "\n".join(baseline.failure_diagnostics(report))
        self.assertIn("Harness Turn 终态：failed", lines)
        self.assertIn("agent.tool_invalid", lines)
        self.assertIn("原执行快照的白名单事实", lines)
        self.assertNotIn("Provider 失败根因", lines)

    def test_missing_legacy_observation_is_not_fabricated(self):
        report, sample = report_with_sample(None)
        baseline.accept_event(report, {"type":"sample","sample":sample})
        self.assertNotIn("preInterruption",report["samples"][0])
        self.assertNotIn("原始 Harness Turn 终态", "\n".join(baseline.failure_diagnostics(report)))

    def test_early_completed_turn_keeps_truth_not_a_false_agent_failure(self):
        report, sample = report_with_sample({"turnStatus":"completed"})
        baseline.accept_event(report, {"type":"sample","sample":sample})
        self.assertIn("Agent 固定错误码：无已认证码", "\n".join(baseline.failure_diagnostics(report)))

    def test_rejects_forged_private_failure_code_or_invalid_state(self):
        cases=[
            {"turnStatus":"unknown","agentErrorCode":"agent.tool_invalid"},
            {"turnStatus":"failed","agentErrorCode":"provider.secret"},
            {"turnStatus":"failed","agentErrorCode":"agent.fake.123"},
            {"turnStatus":"failed","agentErrorCode":"agent.tool_invalid","errorDetail":"api-key-secret"},
            {"turnStatus":"running","agentErrorCode":"agent.tool_invalid"},
            {"turnStatus":"failed","agentErrorCode":True},
            {"turnStatus":True},
            {"turnStatus":[]},
            {"turnStatus":{"status":"failed"}},
            {"turnStatus":"failed","agentErrorCode":[]},
            {"turnStatus":"failed","agentErrorCode":{"code":"agent.tool_invalid"}},
            {"turnStatus":"failed","agentErrorCode":None},
            {"turnStatus":"failed","agentErrorCode":""},
            [],
            None,
        ]
        for bad in cases:
            with self.subTest(case=bad):
                report, sample = report_with_sample(bad)
                if bad is None:
                    sample["preInterruption"] = None
                with self.assertRaisesRegex(baseline.ProtocolFailure,"invalid_preinterruption_evidence"):
                    baseline.accept_event(report,{"type":"sample","sample":sample})
                self.assertEqual(report["samples"], [])

    def test_successful_sample_cannot_claim_preinterruption_failure(self):
        report, sample = report_with_sample({"turnStatus":"failed"})
        sample["status"]="passed"
        sample["stage"]="completed"
        sample["oraclePassed"]=True
        sample.pop("failureCode",None)
        with self.assertRaisesRegex(baseline.ProtocolFailure,"invalid_preinterruption_evidence"):
            baseline.accept_event(report, {"type":"sample","sample":sample})


if __name__ == "__main__":
    unittest.main()
