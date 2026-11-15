"""Offline A/B protocol gates; no Provider calls, credentials or file mutation."""

import copy
import io
import unittest
from unittest import mock

import test_agent_quality as quality
import test_agent_baseline as fixtures

baseline = fixtures.baseline


def pair():
    current = quality.quality_report()
    variant = quality.quality_report(
        quality.quality_config() | {"qualityTreatment": baseline.QUALITY_GENERIC_TREATMENT})
    return current, variant


class QualityTreatmentProtocolTests(unittest.TestCase):
    def test_historical_reports_remain_valid_without_experiment_identity(self):
        historical = fixtures.complete_report()
        baseline.validate_report(historical)
        self.assertNotIn("workloadHash", historical["runner"])
        self.assertNotIn("treatmentHash", historical["runner"])
        historical_quality = quality.quality_report()
        historical_quality["runner"].pop("workloadHash")
        historical_quality["runner"].pop("treatmentHash")
        baseline.validate_report(historical_quality)
        first, second = pair()
        self.assertNotEqual(first["runner"]["datasetHash"], second["runner"]["datasetHash"])
        self.assertEqual(first["runner"]["workloadHash"], second["runner"]["workloadHash"])
        self.assertNotEqual(first["runner"]["treatmentHash"], second["runner"]["treatmentHash"])
        baseline.validate_report(first)
        baseline.validate_report(second)

    def test_invalid_identity_pairs_and_protocol_mutations_fail_closed(self):
        event = quality.quality_start(quality.quality_config())
        config = quality.quality_config()
        for kind, mutation in (
            ("missing_workload", lambda value: value.pop("workloadHash")),
            ("missing_treatment", lambda value: value.pop("treatmentHash")),
            ("uppercase", lambda value: value.update(workloadHash="sha256:" + "A"*64)),
            ("bad_length", lambda value: value.update(workloadHash="sha256:" + "a"*63)),
            ("null", lambda value: value.update(workloadHash=None)),
            ("empty", lambda value: value.update(treatmentHash="")),
            ("boolean", lambda value: value.update(treatmentHash=True)),
            ("unexpected_algorithm", lambda value: value.update(treatmentHash="md5:" + "c"*64)),
        ):
            with self.subTest(kind=kind):
                next_event = copy.deepcopy(event)
                mutation(next_event)
                report = baseline.new_report(config, {})
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_start_identity"):
                    baseline.accept_event(report, next_event)
                self.assertEqual(report["runner"], {})
        experimental_config = quality.quality_config() | {"qualityTreatment": baseline.QUALITY_GENERIC_TREATMENT}
        missing_experiment_identity = quality.quality_start(experimental_config)
        missing_experiment_identity.pop("workloadHash")
        missing_experiment_identity.pop("treatmentHash")
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_start_identity"):
            baseline.accept_event(baseline.new_report(experimental_config, {}), missing_experiment_identity)
        report = quality.quality_report()
        del report["runner"]["treatmentHash"]
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_start_identity"):
            baseline.validate_report(report)

    def test_experiment_treatment_cannot_be_hidden_or_injected_into_legacy_suite(self):
        for value in (None, "", "other", 0, False, {}, []):
            with self.subTest(value=value), self.assertRaises(baseline.SafeFailure):
                baseline.validate_configuration(quality.quality_config() | {"qualityTreatment": value})
        with self.assertRaises(baseline.SafeFailure):
            baseline.validate_configuration(fixtures.configuration() | {
                "qualityTreatment": baseline.QUALITY_GENERIC_TREATMENT})
        with self.assertRaises(baseline.SafeFailure):
            baseline.validate_configuration(quality.quality_config() | {
                "QualityTreatment": baseline.QUALITY_GENERIC_TREATMENT})
        config = quality.quality_config() | {"qualityTreatment": baseline.QUALITY_GENERIC_TREATMENT}
        self.assertEqual(baseline.validate_configuration(config)["qualityTreatment"],
                         baseline.QUALITY_GENERIC_TREATMENT)
        report = baseline.new_report(config, {})
        forged = quality.quality_start(quality.quality_config())
        with self.assertRaisesRegex(baseline.ProtocolFailure, "configuration_mismatch"):
            baseline.accept_event(report, forged)

    def test_comparison_is_explicit_and_holds_model_budgets_and_workload(self):
        first, second = pair()
        strict_text, strict_ok = baseline.comparison_markdown(first, second)
        self.assertFalse(strict_ok)
        self.assertIn("datasetHash", strict_text)
        repeat_text, repeat_ok = baseline.comparison_markdown(first, first)
        self.assertTrue(repeat_ok)
        self.assertNotIn("无法进行数值比较", repeat_text)
        same_text, same_ok = baseline.comparison_markdown(first, first, treatment=True)
        self.assertFalse(same_ok)
        self.assertIn("distinctTreatment", same_text)
        treatment_text, treatment_ok = baseline.comparison_markdown(first, second, treatment=True)
        self.assertTrue(treatment_ok)
        self.assertIn("Agent Treatment A/B", treatment_text)
        self.assertIn("Treatment 哈希", treatment_text)
        self.assertIn("不代表统计显著性", treatment_text)
        self.assertIn("任务组分解", treatment_text)
        changes = [
            ("workload", lambda r: r["runner"].update(workloadHash="sha256:" + "f"*64)),
            ("provider", lambda r: (r["configuration"].update(provider="anthropic"),
                                   r["runner"]["configuration"].update(provider="anthropic"))),
            ("model", lambda r: (r["configuration"].update(model="other"),
                                r["runner"]["configuration"].update(model="other"))),
            ("context", lambda r: (r["configuration"].update(contextTokens=32768),
                                  r["runner"]["configuration"].update(contextTokens=32768))),
            ("budget", lambda r: r["runner"]["profile"].update(maxToolCalls=20)),
            ("dirty", lambda r: r["source"].update(dirty=True)),
            ("different_revision", lambda r: r["source"].update(revision="b" * 40)),
        ]
        for kind, mutation in changes:
            with self.subTest(kind=kind):
                candidate = copy.deepcopy(second)
                mutation(candidate)
                _, comparable = baseline.comparison_markdown(first, candidate, treatment=True)
                self.assertFalse(comparable, "a changed control escaped comparison: " + kind)
        # A new experimental report without both identities is invalid in
        # the strict protocol itself, not merely in the comparison UI.
        missing = copy.deepcopy(second)
        missing["runner"].pop("workloadHash")
        missing["runner"].pop("treatmentHash")
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_start_identity"):
            baseline.comparison_markdown(first, missing, treatment=True)

    def test_read_only_cli_selector_never_starts_provider_in_comparison_mode(self):
        original, variant = pair()
        with mock.patch.object(baseline, "read_report", side_effect=[original, variant]), \
             mock.patch.object(baseline, "launch", side_effect=AssertionError("must not launch")):
            with mock.patch("sys.stdout", new_callable=io.StringIO) as output:
                status = baseline.main(["compare-treatment", "old.json", "new.json"])
            self.assertEqual(status, 0)
            self.assertIn("Agent Treatment A/B", output.getvalue())

    def test_host_variant_cli_requires_explicit_operator_mode(self):
        config = quality.quality_config()
        with mock.patch.object(baseline.sys.stdin, "isatty", return_value=True), \
             mock.patch.object(baseline.sys.stderr, "isatty", return_value=True), \
             mock.patch.object(baseline, "collect_configuration", return_value=config) as collect, \
             mock.patch.object(baseline, "launch", return_value=0) as launch:
            self.assertEqual(baseline.main(["quality-generic"]), 0)
            self.assertEqual(collect.call_args.kwargs["quality_treatment"],
                             baseline.QUALITY_GENERIC_TREATMENT)
            launch.assert_called_once()
        with mock.patch.object(baseline.sys.stdin, "isatty", return_value=True), \
             mock.patch.object(baseline.sys.stderr, "isatty", return_value=True), \
             mock.patch.object(baseline, "collect_configuration", return_value=config) as collect, \
             mock.patch.object(baseline, "launch", return_value=0) as launch:
            self.assertEqual(baseline.main(["pilot-distractor-generic"]), 0)
            self.assertEqual(collect.call_args.kwargs["quality_treatment"],
                             baseline.QUALITY_GENERIC_TREATMENT)
            self.assertEqual(launch.call_args.kwargs["pilot_case_id"], baseline.DISTRACTOR_PILOT_CASE_ID)
            self.assertTrue(launch.call_args.kwargs["pilot_only"])
        self.assertEqual(baseline.main(["pilot-distractor-generic", "old.json"]), 2)


if __name__ == "__main__":
    unittest.main()
