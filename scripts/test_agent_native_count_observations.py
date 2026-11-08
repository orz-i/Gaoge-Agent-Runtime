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

    def test_coarse_count_failure_classes_are_consistent_and_content_free(self):
        report, sample = seeded_sample()
        sample["inputCountObservations"]["unavailableReasons"] = {
            "localRejected": 0, "http4xx": 1, "http5xx": 0,
            "otherHTTP": 0, "transportOrProtocol": 0, "other": 0,
        }
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        lines = "\n".join(baseline.failure_diagnostics(report))
        self.assertIn("HTTP 4xx=1", lines)
        self.assertIn("本地拒绝=0", lines)
        self.assertIn("非模型生成", lines)
        self.assertNotIn("private-api-key", lines)
        self.assertEqual(report["samples"][0]["inputCountObservations"]["unavailable"], 1)

    def test_rejects_forged_or_inconsistent_count_failure_classes(self):
        base = {
            "localRejected": 0, "http4xx": 1, "http5xx": 0,
            "otherHTTP": 0, "transportOrProtocol": 0, "other": 0,
        }
        variants = {
            "missing-category": lambda v: v.pop("http4xx"),
            "raw-provider-message": lambda v: v.update({"statusBody": "private-api-key"}),
            "negative": lambda v: v.update({"http4xx": -1}),
            "boolean": lambda v: v.update({"http4xx": True}),
            "fractional": lambda v: v.update({"http4xx": 1.0}),
            "wrong-partition": lambda v: v.update({"other": 1}),
            "out-of-range": lambda v: v.update({"http4xx": 4}),
        }
        for name, change in variants.items():
            with self.subTest(name=name):
                report, sample = seeded_sample()
                reasons = copy.deepcopy(base)
                change(reasons)
                sample["inputCountObservations"]["unavailableReasons"] = reasons
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_observations"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})
                self.assertEqual(report["samples"], [])

        for bad in (None, [], "http4xx", True, 3):
            with self.subTest(type=type(bad).__name__):
                report, sample = seeded_sample()
                sample["inputCountObservations"]["unavailableReasons"] = bad
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_observations"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})

    def test_native_count_usage_comparison_is_exact_request_only(self):
        report, sample = seeded_sample()
        # This synthetic task has four calls but originally reported no
        # Provider Usage; supply one known physical receipt for the single
        # identity-matched comparison, with three calls still unknown.
        sample["usage"] = {
            "knownCalls": 1, "missingCalls": 3, "complete": False,
            "known": {"inputTokens": 57, "outputTokens": 8,
                      "cacheReadTokens": 0, "cacheWriteTokens": 0, "reasoningTokens": 0},
        }
        sample["inputCountComparison"] = {
            "compared": 1, "equal": 0, "belowUsage": 1, "aboveUsage": 0,
            "maxUnderestimate": 13,
        }
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        diagnostics = "\n".join(baseline.failure_diagnostics(report))
        self.assertIn("计数低于实际 Usage=1", diagnostics)
        self.assertIn("最大已观测低估=13 Tokens", diagnostics)
        self.assertIn("低估属于计量准入阻断证据", diagnostics)
        self.assertNotIn("rawRequest", diagnostics)

    def test_cannot_reuse_single_accepted_count_as_multiple_matches(self):
        report, sample = seeded_sample()
        sample["inputCountObservations"] = {"attempts": 3, "accepted": 1, "unavailable": 2}
        sample["usage"] = {
            "knownCalls": 2, "missingCalls": 2, "complete": False,
            "known": {"inputTokens": 100, "outputTokens": 11,
                      "cacheReadTokens": 0, "cacheWriteTokens": 0, "reasoningTokens": 0},
        }
        sample["inputCountComparison"] = {
            "compared": 2, "equal": 2, "belowUsage": 0,
            "aboveUsage": 0, "maxUnderestimate": 0,
        }
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_comparison"):
            baseline.accept_event(report, {"type": "sample", "sample": sample})
        self.assertEqual(report["samples"], [])

    def test_unmatched_old_count_report_is_not_assumed_accurate(self):
        report, sample = seeded_sample()
        baseline.accept_event(report, {"type": "sample", "sample": sample})
        self.assertNotIn("inputCountComparison", report["samples"][0])
        self.assertNotIn("原生计数与随后相同输入", "\n".join(baseline.failure_diagnostics(report)))

    def test_rejects_fabricated_or_inconsistent_count_usage_matching(self):
        base = {"compared": 1, "equal": 1, "belowUsage": 0,
                "aboveUsage": 0, "maxUnderestimate": 0}
        changes = {
            "missing-key": lambda value: value.pop("aboveUsage"),
            "raw-hash-injection": lambda value: value.update({"fingerprint": "sha256:private"}),
            "multiple-outcomes": lambda value: value.update({"belowUsage": 1}),
            "boolean": lambda value: value.update({"compared": True}),
            "negative": lambda value: value.update({"equal": -1}),
            "float": lambda value: value.update({"equal": 1.0}),
            "underestimate-with-no-under": lambda value: value.update({"maxUnderestimate": 15}),
            "missing-under-delta": lambda value: value.update(
                {"equal": 0, "belowUsage": 1, "maxUnderestimate": 0}),
            "more-than-models": lambda value: value.update({"compared": 100, "equal": 100}),
            "too-large-delta": lambda value: value.update(
                {"equal": 0, "belowUsage": 1, "maxUnderestimate": 1 << 52}),
        }
        for name, mutation in changes.items():
            with self.subTest(name=name):
                report, sample = seeded_sample()
                value = copy.deepcopy(base)
                mutation(value)
                sample["inputCountComparison"] = value
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_comparison"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})
                self.assertEqual(report["samples"], [])

        for bad in (None, [], "matched", True):
            with self.subTest(type=type(bad).__name__):
                report, sample = seeded_sample()
                sample["inputCountComparison"] = bad
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_comparison"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})

        report, sample = seeded_sample()
        sample.pop("inputCountObservations")
        sample["inputCountComparison"] = copy.deepcopy(base)
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_comparison"):
            baseline.accept_event(report, {"type": "sample", "sample": sample})

    def test_legacy_or_anthropic_cannot_claim_openai_native_proof(self):
        for provider, suite in (("openai", False), ("anthropic", True), ("anthropic", False)):
            with self.subTest(provider=provider, suite=suite):
                report, sample = seeded_sample(provider, suite)
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_input_count_observations"):
                    baseline.accept_event(report, {"type": "sample", "sample": sample})

if __name__ == "__main__":
    unittest.main()
