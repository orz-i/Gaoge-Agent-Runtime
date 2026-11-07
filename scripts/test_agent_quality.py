"""Offline protocol and report tests for the opt-in 40-case effect benchmark."""

import contextlib
import copy
import io
import json
import math
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import test_agent_baseline as fixtures

baseline = fixtures.baseline


def quality_config(repetitions=1):
    return dict(fixtures.configuration(), suite=baseline.QUALITY_SUITE, repetitions=repetitions)


def quality_start(config):
    event = fixtures.start_event(config)
    event["taskVersion"] = "release-evidence-quality-v1"
    event["requestedSamples"] = len(baseline.QUALITY_CASE_IDS) * config["repetitions"]
    return event


def quality_samples(config, failed_case=""):
    result = []
    index = 0
    for repetition in range(1, config["repetitions"] + 1):
        for group in baseline.QUALITY_GROUPS:
            for n in range(1, 9):
                index += 1
                case_id = f"{group}-{n:02d}"
                item = fixtures.sample(index, passed=case_id != failed_case)
                item["caseID"] = case_id
                item["repetition"] = repetition
                item["sampleID"] = f"{case_id}-{repetition:02d}"
                result.append(item)
    return result


def quality_report(config=None, failed_case=""):
    config = quality_config() if config is None else config
    report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
    baseline.accept_event(report, quality_start(config))
    baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 4, "usage": None})
    samples = quality_samples(config, failed_case)
    for item in samples:
        baseline.accept_event(report, {"type": "sample", "sample": item})
    attempted = len(samples)
    passed = sum(item["status"] == "passed" for item in samples)
    lengths = sorted(item["durationMs"] for item in samples)
    duration = {
        "count": attempted, "total": sum(lengths), "min": lengths[0],
        "max": lengths[-1], "p50": lengths[math.ceil(.5 * attempted) - 1],
        "p95": lengths[math.ceil(.95 * attempted) - 1],
    }
    baseline.accept_event(report, {"type": "summary", "summary": {
        "requested": attempted, "attempted": attempted, "passed": passed,
        "failed": attempted - passed, "skipped": 0, "complete": True,
        "successRate": passed / attempted, "durationMs": duration,
        "modelCalls": 4 * attempted, "toolCalls": 3 * attempted,
        "usage": fixtures.usage(4 * attempted),
    }})
    baseline.finish_report(report, 0 if passed == attempted else 1)
    return report


class QualitySuiteTests(unittest.TestCase):
    def test_quality_case_identity_and_explicit_config_controls(self):
        self.assertEqual(len(baseline.QUALITY_CASE_IDS), 40)
        self.assertEqual(len(baseline.QUALITY_GROUPS), 5)
        good = baseline.validate_configuration(quality_config())
        self.assertEqual(good["suite"], baseline.QUALITY_SUITE)
        self.assertEqual(baseline.validate_configuration(fixtures.configuration()).get("suite"), None)
        for value in (None, "", "legacy-2", "other", True, 0, [], {}):
            with self.subTest(value=value):
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_configuration(dict(fixtures.configuration(), suite=value))
        for repetitions in (0, 4, 20):
            with self.subTest(repetitions=repetitions):
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_configuration(quality_config(repetitions))
        with self.assertRaises(baseline.SafeFailure):
            baseline.validate_configuration(dict(fixtures.configuration(), Suite=baseline.QUALITY_SUITE))

    def test_full_forty_case_event_protocol_and_redacted_group_report(self):
        report = quality_report(failed_case="unicode-03")
        baseline.validate_report(report)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["summary"]["attempted"], 40)
        self.assertEqual(report["summary"]["passed"], 39)
        self.assertEqual(report["summary"]["modelCalls"], 160)
        self.assertEqual(len({sample["caseID"] for sample in report["samples"]}), 40)
        markdown = baseline.render_markdown(report)
        self.assertIn("Evidence-quality 分组观测", markdown)
        self.assertIn("unicode", markdown)
        self.assertIn("7 / 8", markdown)
        self.assertIn("Token 用量", markdown)
        self.assertIn("未知", markdown)
        self.assertIn("40 例属于同一证据任务族", markdown)

    def test_quality_report_repetition_three_caps_at_120_and_remains_comparable(self):
        config = quality_config(3)
        report = quality_report(config)
        baseline.validate_report(report)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["summary"]["requested"], 120)
        second = copy.deepcopy(report)
        second["source"]["revision"] = "b" * 40
        rendered, complete = baseline.comparison_markdown(report, second)
        self.assertTrue(complete)
        self.assertIn("任务组分解", rendered)
        self.assertIn("40 例属于同一证据任务族", rendered)

    def test_quality_report_does_not_masquerade_as_legacy_or_accept_unknown_case(self):
        config = quality_config()
        report = baseline.new_report(config, {})
        old = fixtures.start_event(fixtures.configuration())
        with self.assertRaises(baseline.ProtocolFailure):
            baseline.accept_event(report, old)
        self.assertEqual(report["runner"], {})
        baseline.accept_event(report, quality_start(config))
        baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 1, "usage": None})
        invalid = fixtures.sample()
        invalid["caseID"] = "unknown-01"
        with self.assertRaisesRegex(baseline.ProtocolFailure, "unexpected_case"):
            baseline.accept_event(report, {"type": "sample", "sample": invalid})
        self.assertEqual(report["samples"], [])
        report["runner"]["taskVersion"] = "release-recovery-v1"
        with self.assertRaises(baseline.ProtocolFailure):
            baseline.validate_report(report)

        legacy = fixtures.complete_report()
        quality = quality_report()
        rendered, comparable = baseline.comparison_markdown(legacy, quality)
        self.assertFalse(comparable)
        self.assertIn("无法进行数值比较", rendered)
        self.assertIn("suite", rendered)

    def test_partial_quality_reports_do_not_invent_skipped_success(self):
        config = quality_config()
        report = baseline.new_report(config, {})
        baseline.accept_event(report, quality_start(config))
        baseline.accept_event(report, {"type": "preflight", "status": "passed", "durationMs": 1, "usage": None})
        first = quality_samples(config)[0]
        baseline.accept_event(report, {"type": "sample", "sample": first})
        baseline.accept_event(report, {"type": "summary", "summary": {
            "requested": 40, "attempted": 1, "passed": 1, "failed": 0,
            "skipped": 39, "complete": False, "successRate": 1.0,
            "durationMs": {"count": 1, "total": first["durationMs"], "min": first["durationMs"],
                           "max": first["durationMs"], "p50": first["durationMs"], "p95": first["durationMs"]},
            "modelCalls": 4, "toolCalls": 3, "usage": fixtures.usage(4),
        }})
        baseline.finish_report(report, 1)
        self.assertEqual(report["status"], "incomplete")
        baseline.validate_report(report)
        self.assertIn("未执行 39", baseline.render_markdown(report))
        self.assertIn("1 / 1", baseline.render_markdown(report))


class CapabilityAdmissionDiagnosisTests(unittest.TestCase):
    def test_missing_host_model_profile_explains_local_preflight_denial(self):
        explanation = baseline.failure_advice("preflight", "capability_unavailable", {})
        for expected in (
            "GAOGE_MODEL_PROFILES_FILE", "StructuredJSONSchema", "SupportsForcedToolChoice",
            "EffortLevels", "reasoning.effort", "Fail-Closed",
        ):
            self.assertIn(expected, explanation)
        self.assertNotIn("API key", explanation)
        self.assertNotIn("private-fixture-key", explanation)
        schema_advice = baseline.failure_advice("preflight", "output_schema_invalid", {})
        self.assertNotIn("GAOGE_MODEL_PROFILES_FILE", schema_advice)
        self.assertIn("JSON Schema", schema_advice)



class ToolWireEvidenceTests(unittest.TestCase):
    def record(self, evidence):
        config = fixtures.configuration()
        start = fixtures.start_event(config)
        start["profile"]["preflightVersion"] = baseline.OPENAI_TOOL_PROBE_PROFILE
        report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
        baseline.accept_event(report, start)
        probe = {
            "status": "failed", "stage": "tool_call",
            "failureCode": "tool_probe_provider_failed",
            "modelCalls": 1, "httpRequests": 1,
            "usage": fixtures.usage(1),
            "reasoningSeen": False, "encryptedReplaySeen": False,
            "httpStatus": 200, "responseKind": "event_stream",
            "responseDebug": fixtures.response_debug_fixture(
                body='event: response.completed\\ndata: {\"reasoning\":\"[REDACTED]\"}\\n'
            ),
            "wireEvidence": evidence,
        }
        preflight = {
            "type": "preflight", "status": "failed", "failureCode": "tool_probe_provider_failed",
            "durationMs": 10, "usage": None, "httpStatus": 200,
            "responseKind": "json", "toolProbe": probe,
        }
        return report, preflight

    def test_content_free_ciphertext_consistency_is_reportable_without_replay_authority(self):
        facts = {
            "reasoningDone": True, "encryptedDone": True,
            "terminalSeen": True, "doneFinalMatch": False,
        }
        report, event = self.record(facts)
        baseline.accept_event(report, event)
        summary = fixtures.events(preflight_passed=False)[-1]
        summary["summary"]["requested"] = 2
        baseline.accept_event(report, summary)
        baseline.finish_report(report, 1)
        self.assertEqual(report["status"], "failed")
        baseline.validate_report(report)
        rendered = baseline.render_markdown(report)
        self.assertIn("done/final 加密字段一致=否", rendered)
        self.assertIn("密文与 ID 均不记录", rendered)
        self.assertIn("已接受的推理/回放证据：reasoning=否", rendered)
        self.assertNotIn("server-secret", rendered)

    def test_tool_probe_failure_advice_distinguishes_verified_mismatch_from_unknown(self):
        base = {"toolProbe": {"wireEvidence": {"reasoningDone": True,
                 "encryptedDone": True, "terminalSeen": True, "doneFinalMatch": False}}}
        mismatch = baseline.failure_advice("preflight", "tool_probe_provider_failed", base)
        self.assertIn("加密内容不同", mismatch)
        self.assertIn("output_item.done", mismatch)
        self.assertIn("不能证明响应非法", mismatch)
        self.assertIn("不要", mismatch)
        base["toolProbe"]["wireEvidence"]["doneFinalMatch"] = True
        match = baseline.failure_advice("preflight", "tool_probe_provider_failed", base)
        self.assertIn("加密字段一致", match)
        self.assertNotEqual(match, mismatch)
        unknown = baseline.failure_advice("preflight", "tool_probe_provider_failed", {})
        self.assertIn("无法证实", unknown)
        self.assertIn("Fail-Closed", unknown)

    def test_wire_evidence_is_bounded_typed_and_cannot_carry_raw_provider_data(self):
        base = {
            "reasoningDone": True, "encryptedDone": True,
            "terminalSeen": True, "doneFinalMatch": True,
        }
        bad = [
            dict(base, doneFinalMatch="false"),
            dict(base, encryptedDone="true"),
            dict(base, reasoningDone=False),
            dict(base, terminalSeen=False),
            dict(base, cipher="server-secret-must-not-leak"),
            dict(base, doneFinalMatch=None),
            {"reasoningDone": True, "encryptedDone": True},
        ]
        for mutation in bad:
            with self.subTest(value=mutation):
                report, event = self.record(mutation)
                with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_tool_probe"):
                    baseline.accept_event(report, event)
                self.assertIsNone(report["preflight"])
        report, event = self.record({
            "reasoningDone": True, "encryptedDone": True, "terminalSeen": False
        })
        baseline.accept_event(report, event)
        self.assertEqual(
            report["preflight"]["toolProbe"]["wireEvidence"]["terminalSeen"], False
        )

def preflight_only_events(config, passed=True):
    start = quality_start(config)
    start["preflightOnly"] = True
    preflight = {"type": "preflight", "status": "passed" if passed else "failed",
                 "durationMs": 8, "usage": None}
    if not passed:
        preflight["failureCode"] = "capability_unavailable"
    requested = len(baseline.QUALITY_CASE_IDS) * config["repetitions"]
    summary = {"type": "summary", "summary": {
        "requested": requested, "attempted": 0, "passed": 0, "failed": 0,
        "skipped": requested, "complete": False, "successRate": None,
        "durationMs": {"count": 0, "total": 0, "min": 0, "max": 0, "p50": 0, "p95": 0},
        "usage": fixtures.usage(0), "modelCalls": 0, "toolCalls": 0,
    }}
    return [start, preflight, summary]


class PreflightOnlyTests(unittest.TestCase):
    def test_probe_only_report_has_no_task_grade_and_remains_readable(self):
        config = quality_config()
        with tempfile.TemporaryDirectory() as directory:
            writer = baseline.ReportWriter(Path(directory) / "reports")
            report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
            program = fixtures.child_program(preflight_only_events(config), exit_code=0)
            with contextlib.redirect_stdout(io.StringIO()):
                result = baseline.run_child(
                    [sys.executable, "-u", "-c", program], Path(directory), config,
                    "fake-only-stdin-secret", report, writer, preflight_only=True)
            self.assertEqual(result["status"], "preflight_only_passed")
            self.assertEqual(result["samples"], [])
            self.assertEqual(result["summary"]["skipped"], 40)
            self.assertIsNone(result["summary"]["successRate"])
            self.assertTrue(result["runner"]["preflightOnly"])
            restored = baseline.read_report(writer.json_path)
            self.assertEqual(restored["status"], "preflight_only_passed")
            self.assertNotIn("fake-only-stdin-secret", writer.json_path.read_text())
            self.assertIn("仅执行 Provider 能力预检", writer.md_path.read_text())
            rendered, comparable = baseline.comparison_markdown(restored, quality_report())
            self.assertFalse(comparable)
            self.assertIn("preflightOnly", rendered)
            tampered = copy.deepcopy(restored)
            tampered["status"] = "passed"
            with self.assertRaisesRegex(baseline.SafeFailure, "report_status_mismatch"):
                baseline.validate_report(tampered)

    def test_probe_only_failure_still_is_failure_without_task_attempt(self):
        config = quality_config()
        report = baseline.new_report(config, {})
        for event in preflight_only_events(config, passed=False):
            baseline.accept_event(report, event)
        baseline.finish_report(report, 1)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["samples"], [])
        baseline.validate_report(report)

    def test_probe_only_cannot_append_samples_or_forge_successful_summary(self):
        config = quality_config()
        events = preflight_only_events(config)
        for mutation in (
            lambda record: record.update(attempted=1, passed=1, skipped=39),
            lambda record: record.update(complete=True),
            lambda record: record.update(successRate=1.0),
            lambda record: record["durationMs"].update(total=10),
            lambda record: record.update(modelCalls=1),
            lambda record: record.update(usage=fixtures.usage(1)),
        ):
            with self.subTest(mutation=mutation):
                report = baseline.new_report(config, {})
                for event in events[:2]:
                    baseline.accept_event(report, event)
                bad = copy.deepcopy(events[2])
                mutation(bad["summary"])
                with self.assertRaises(baseline.ProtocolFailure):
                    baseline.accept_event(report, bad)
                self.assertIsNone(report["summary"])
        report = baseline.new_report(config, {})
        for event in events[:2]:
            baseline.accept_event(report, event)
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_sample_order"):
            baseline.accept_event(report, {"type": "sample", "sample": quality_samples(config)[0]})
        for invalid in (False, None, "true"):
            report = baseline.new_report(config, {})
            event = dict(events[0], preflightOnly=invalid)
            with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_start"):
                baseline.accept_event(report, event)

    def test_runner_mode_is_bound_to_expected_start_event(self):
        config = quality_config()
        for declared, expected in ((True, False), (False, True)):
            with self.subTest(declared=declared), tempfile.TemporaryDirectory() as directory:
                writer = baseline.ReportWriter(Path(directory) / "reports")
                report = baseline.new_report(config, {})
                events = preflight_only_events(config)
                if not declared:
                    del events[0]["preflightOnly"]
                program = fixtures.child_program(events, exit_code=0)
                with contextlib.redirect_stdout(io.StringIO()):
                    result = baseline.run_child([sys.executable, "-u", "-c", program],
                                                Path(directory), config, "fake-key", report, writer,
                                                preflight_only=expected)
                self.assertNotIn(result["status"], ("passed", "preflight_only_passed"))
                self.assertEqual(result["launcherFailure"]["failureCode"], "execution_mode_mismatch")
                baseline.read_report(writer.json_path)

    def test_launch_probe_never_uses_full_task_command_or_inherited_key(self):
        config = quality_config()
        real_run_child = baseline.run_child
        seen = []

        def fixture_runner(command, root, actual, secret, report, writer, **kwargs):
            self.assertEqual(command[1:], ["-mode=baseline-preflight-stdin"])
            self.assertEqual(secret, "fresh-hidden-test-key")
            self.assertTrue(kwargs.get("preflight_only"))
            seen.append("run")
            program = fixtures.child_program(preflight_only_events(actual), exit_code=0)
            return real_run_child([sys.executable, "-u", "-c", program], root, actual, secret,
                                  report, writer, preflight_only=True)

        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            result = baseline.launch(config, Path(directory),
                                     get_key=lambda: (seen.append("key") or "fresh-hidden-test-key"),
                                     builder=lambda *_: seen.append("build"), preflight_only=True)
            self.assertEqual(result, 0)
            self.assertEqual(seen, ["build", "key", "run"])
            self.assertIn("任务样本 0 已执行", output.getvalue())
            reports = list((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            self.assertEqual(len(reports), 1)
            self.assertEqual(baseline.read_report(reports[0])["status"], "preflight_only_passed")
            self.assertNotIn("fresh-hidden-test-key", reports[0].read_text())
            with contextlib.redirect_stderr(io.StringIO()) as denied, \
                    mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "launch") as no_full_task_launch:
                self.assertEqual(baseline.main(["retry", str(reports[0])]), 2)
                no_full_task_launch.assert_not_called()
                self.assertIn("probe_report_requires_explicit_quality", denied.getvalue())

    def test_probe_cli_reuses_historical_public_config_and_fails_closed_without_tty(self):
        config = quality_config()
        prior = quality_report(config)
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "old-report.json"
            source.write_text(json.dumps(prior), encoding="utf-8")
            with mock.patch.object(sys.stdin, "isatty", return_value=False), \
                    mock.patch.object(baseline, "launch") as launcher, \
                    contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(baseline.main(["probe", str(source)]), 2)
                launcher.assert_not_called()
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "repository_root", return_value=Path(directory)), \
                    mock.patch.object(baseline, "launch", return_value=0) as launcher, \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(baseline.main(["probe", str(source)]), 0)
                self.assertTrue(launcher.call_args.kwargs["preflight_only"])
                self.assertEqual(launcher.call_args.args[0], config)
                self.assertIn("不会运行任何任务样本", output.getvalue())
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "collect_configuration", return_value=config) as collect, \
                    mock.patch.object(baseline, "launch", return_value=0) as launcher:
                self.assertEqual(baseline.main(["probe"]), 0)
                collect.assert_called_once_with(baseline.QUALITY_SUITE, preflight_only=True)
                self.assertTrue(launcher.call_args.kwargs["preflight_only"])
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(baseline.main(["probe", "a", "extra"]), 2)


if __name__ == "__main__":
    unittest.main()
