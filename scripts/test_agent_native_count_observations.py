"""Quality-only, content-free native InputTokenCounter observation contract."""
import copy
import unittest

import test_agent_quality as quality
import test_agent_baseline as fixture

baseline = fixture.baseline


def seeded_sample(provider="openai", suite=True):
    config = fixture.configuration()
    config["provider"] = provider
    if suite:
        config["suite"] = baseline.QUALITY_SUITE
    report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
    start = quality.quality_start(config) if suite else fixture.start_event(config)
    baseline.accept_event(report, start)
    baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 3, "usage": None})
    sample = quality.quality_samples(quality.quality_config(), failed_case="distractor-05")[20] if suite else fixture.sample()
    sample["inputCountObservations"] = {"attempts": 3, "accepted": 2, "unavailable": 1}
    return report, sample


class NativeInputCountObservationsTests(unittest.TestCase):
    def test_admitted_quality_only_without_http_or_token_usage_inference(self):
        report, sample = seeded_sample()
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        saved = report["samples"][0]["inputCountObservations"]
        self.assertEqual(saved, {"attempts": 3, "accepted": 2, "unavailable": 1})
        lines = "\n".join(baseline.failure_diagnostics(report))
        self.assertIn("尝试=3", lines)
        self.assertIn("接受正数计数=2", lines)
        self.assertIn("不可用=1", lines)
        self.assertIn("不等于真实 HTTP 请求次数", lines)
        self.assertNotIn("因计数失败导致", lines)
        self.assertNotIn("Token 费用=", lines)

    def test_missing_optional_on_historical_report_is_not_backfilled(self):
        report, sample = seeded_sample()
        del sample["inputCountObservations"]
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        self.assertNotIn("inputCountObservations", report["samples"][0])
        self.assertNotIn("原生计数器调用", "\n".join(baseline.failure_diagnostics(report)))

    def test_rejects_forged_or_ambiguous_counts(self):
        mutations = {
            "missing": lambda v: v.pop("unavailable"),
            "extra": lambda v: v.update({"apiKey": "sensitive"}),
            "wrong-total": lambda v: v.update({"attempts": 4}),
            "zero-attempts": lambda v: v.update({"attempts": 0, "accepted": 0, "unavailable": 0}),
            "negative": lambda v: v.update({"accepted": -1, "unavailable": 4}),
            "bool": lambda v: v.update({"accepted": True, "unavailable": 2}),
            "float": lambda v: v.update({"accepted": 2.0}),
            "huge": lambda v: v.update({"attempts": 1<<30, "accepted": 1<<30, "unavailable": 0}),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name):
                report, sample = seeded_sample()
                mutation(sample["inputCountObservations"])
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_observations"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})
                self.assertEqual(report["samples"], [])

    def test_legacy_or_anthropic_cannot_claim_openai_native_proof(self):
        for provider, suite in (("openai", False), ("anthropic", True), ("anthropic", False)):
            with self.subTest(provider=provider, suite=suite):
                report, sample = seeded_sample(provider, suite)
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_observations"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})

if __name__ == "__main__":
    unittest.main()
