"""Bounded, privacy-safe evidence of whether an Agent searched or paginated.

All tests use synthetic report fixtures; no secrets, network or actual reports.
"""
import copy
import unittest

import test_agent_quality as quality
from test_agent_baseline import baseline


SEARCHED = {
    "queried": 1, "offsetOnly": 0, "candidatePages": 0,
    "normalizedHintPages": 0, "hintedOffsetPages": 0, "donePages": 0,
}


def single_sample_report(modes=SEARCHED):
    config = quality.quality_config()
    report = baseline.new_report(config, {"revision": "f" * 40, "dirty": False})
    baseline.accept_event(report, quality.quality_start(config))
    baseline.accept_event(report, {
        "type": "preflight", "status": "passed", "durationMs": 3, "usage": None,
    })
    sample = quality.quality_samples(config, failed_case="distractor-05")[20]
    assert sample["caseID"] == "distractor-05"
    if modes is not None:
        sample["artifactReadStrategy"] = copy.deepcopy(modes)
    return report, sample


class ArtifactStrategyReportTests(unittest.TestCase):
    def test_current_quality_report_accepts_safe_counts_and_displays_only_measured_modes(self):
        report, sample = single_sample_report()
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        diagnostics = "\n".join(baseline.failure_diagnostics(report))
        self.assertIn("Artifact 读取方式", diagnostics)
        self.assertIn("query=1", diagnostics)
        self.assertIn("offset-only=0", diagnostics)
        self.assertIn("工具路径事实", diagnostics)
        self.assertNotIn("artifactID", diagnostics)
        self.assertNotIn("DEPLOYMENT_CODE", diagnostics)
        self.assertNotIn("R-17", diagnostics)
        self.assertNotIn("失败根因：", diagnostics)

    def test_old_reports_with_no_extra_metrics_remain_readable_and_do_not_invent_counts(self):
        report, sample = single_sample_report(None)
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        self.assertNotIn("Artifact 读取方式", "\n".join(baseline.failure_diagnostics(report)))
        old = quality.quality_report(failed_case="distractor-05")
        baseline.validate_report(old)
        self.assertNotIn("artifactReadStrategy", old["samples"][0])

    def test_rejects_malformed_or_forged_mode_accounting(self):
        mutations = {
            "missing-count": lambda m: m.pop("queried"),
            "extra-text": lambda m: m.update({"rawQuery": "RELEASE R-17"}),
            "negative-count": lambda m: m.update({"queried": -1}),
            "boolean-count": lambda m: m.update({"queried": True}),
            "wrong-total": lambda m: m.update({"offsetOnly": 1}),
            "unauthorized-candidates": lambda m: m.update({"candidatePages": 1, "queried": 0, "offsetOnly": 1}),
            "orphaned-normalized-hint": lambda m: m.update({"normalizedHintPages": 1}),
            "orphaned-pagination-hint": lambda m: m.update({"hintedOffsetPages": 1}),
            "excess-done-pages": lambda m: m.update({"donePages": 2}),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                modes = copy.deepcopy(SEARCHED)
                mutate(modes)
                report, sample = single_sample_report(modes)
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_artifact_read_strategy"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})
                self.assertEqual(report["samples"], [])

    def test_legacy_suite_must_not_claim_quality_only_strategy_metrics(self):
        import test_agent_baseline as fixture
        config = fixture.configuration()
        report = baseline.new_report(config, {"revision": "c" * 40, "dirty": False})
        baseline.accept_event(report, fixture.start_event(config))
        baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 1, "usage": None})
        first = fixture.sample()
        first["artifactReadStrategy"] = copy.deepcopy(SEARCHED)
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_artifact_read_strategy"):
            baseline.accept_event(report, {"type": "sample", "sample": first})
        self.assertEqual(report["samples"], [])

    def test_mixed_retrieval_modes_still_consistently_account_for_each_read(self):
        report, sample = single_sample_report({
            "queried": 2, "offsetOnly": 1, "candidatePages": 1,
            "normalizedHintPages": 0, "hintedOffsetPages": 1, "donePages": 0,
        })
        # Recreate a completed handler count with a correspondingly larger
        # trusted, synthetic Tool count. Still no raw query or source bytes.
        sample["artifactReads"] = 3
        sample["toolCalls"] = 5
        sample["toolExecutions"][1]["executions"] = 3
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        self.assertEqual(report["samples"][0]["artifactReadStrategy"]["offsetOnly"], 1)

if __name__ == "__main__":
    unittest.main()
