"""Fail-closed first real-task pilot admission and report protocol tests."""

import contextlib
import copy
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import test_agent_baseline as fixture
import test_agent_quality as quality

baseline = fixture.baseline


def pilot_events(config=None, passed=True, preflight_passed=True, case_id=baseline.PILOT_CASE_ID):
    config = quality.quality_config() if config is None else config
    start = quality.quality_start(config)
    start.update(pilotOnly=True, pilotCaseID=case_id)
    preflight = {"type": "preflight", "status": "passed" if preflight_passed else "failed",
                 "durationMs": 5, "usage": None}
    if not preflight_passed:
        preflight["failureCode"] = "capability_unavailable"
    sample = fixture.sample(1, passed=passed)
    sample["caseID"] = case_id
    sample["sampleID"] = case_id + "-01"
    samples = [sample] if preflight_passed else []
    count = len(samples)
    duration = sample["durationMs"] if count else 0
    summary = {"type": "summary", "summary": {
        "requested": 40, "attempted": count, "passed": int(passed) * count,
        "failed": int(not passed) * count, "skipped": 40 - count,
        "complete": False, "successRate": (float(passed) if count else None),
        "durationMs": {"count": count, "total": duration, "min": duration,
                       "max": duration, "p50": duration, "p95": duration},
        "modelCalls": 4 * count, "toolCalls": 3 * count,
        "usage": fixture.usage(4 * count),
    }}
    return [start, preflight] + [
        {"type": "sample", "sample": item} for item in samples
    ] + [summary]


def pilot_report(passed=True, preflight_passed=True):
    config = quality.quality_config()
    report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
    for event in pilot_events(config, passed=passed, preflight_passed=preflight_passed):
        baseline.accept_event(report, event)
    code = 0 if passed and preflight_passed else 1
    baseline.finish_report(report, code)
    return report


class FirstLivePilotTests(unittest.TestCase):
    def test_distractor_pilot_report_and_single_case_identity(self):
        config = quality.quality_config()
        report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
        events = pilot_events(config, case_id=baseline.DISTRACTOR_PILOT_CASE_ID)
        for event in events:
            baseline.accept_event(report, event)
        baseline.finish_report(report, 0)
        self.assertEqual(report["status"], "pilot_sample_passed")
        self.assertEqual(report["runner"]["pilotCaseID"], "distractor-05")
        markdown = baseline.render_markdown(report)
        self.assertIn("本报告只试运行固定案例 distractor-05", markdown)
        self.assertNotIn("本报告只试运行固定案例 code-01", markdown)
        self.assertEqual(len(report["samples"]), 1)
        self.assertEqual(report["samples"][0]["caseID"], "distractor-05")
        self.assertFalse(report["summary"]["complete"])
        self.assertEqual(report["summary"]["skipped"], 39)
        baseline.validate_report(report)
        with self.assertRaises(baseline.ProtocolFailure):
            invalid = baseline.new_report(config, {"revision": "b" * 40, "dirty": False})
            for event in pilot_events(config, case_id="distractor-06"):
                baseline.accept_event(invalid, event)
        for wrong in ("code-01", "distractor-01", "distractor-05-02"):
            forged = pilot_events(config, case_id=baseline.DISTRACTOR_PILOT_CASE_ID)
            forged[2]["sample"]["sampleID"] = wrong
            invalid = baseline.new_report(config, {"revision": "c" * 40, "dirty": False})
            with self.assertRaises(baseline.ProtocolFailure):
                for event in forged:
                    baseline.accept_event(invalid, event)

    def test_distractor_pilot_launcher_fences_child_case_and_cli(self):
        config = quality.quality_config()
        mode = []
        original_child = baseline.run_child
        def intercepted(command, root, selected, secret, report, writer, **kwargs):
            self.assertEqual(command[1:], ["-mode=baseline-distractor-pilot-stdin"])
            self.assertEqual(kwargs["pilot_case_id"], baseline.DISTRACTOR_PILOT_CASE_ID)
            mode.append("child")
            program = fixture.child_program(pilot_events(selected, case_id=baseline.DISTRACTOR_PILOT_CASE_ID), exit_code=0)
            return original_child([sys.executable, "-u", "-c", program], root, selected, secret,
                                  report, writer, pilot_only=True, pilot_case_id=baseline.DISTRACTOR_PILOT_CASE_ID)
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=intercepted), \
                contextlib.redirect_stdout(io.StringIO()):
            exit_code = baseline.launch(config, Path(directory), get_key=lambda:"fake-key",
                                        builder=lambda *_:None, pilot_only=True,
                                        pilot_case_id=baseline.DISTRACTOR_PILOT_CASE_ID)
            self.assertEqual(exit_code, 0)
            self.assertEqual(mode, ["child"])
            saved = next((Path(directory)/"artifacts"/"agent-baselines").glob("*.json"))
            self.assertEqual(baseline.read_report(saved)["runner"]["pilotCaseID"], "distractor-05")
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "collect_configuration", return_value=config), \
                    mock.patch.object(baseline, "launch", return_value=0) as launched:
                self.assertEqual(baseline.main(["pilot-distractor"]), 0)
                self.assertEqual(launched.call_args.kwargs["pilot_case_id"], "distractor-05")
        mismatch = pilot_events(config, case_id=baseline.PILOT_CASE_ID)
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                contextlib.redirect_stdout(io.StringIO()):
            report=baseline.new_report(config,{})
            writer=baseline.ReportWriter(Path(directory))
            program=fixture.child_program(mismatch, exit_code=0)
            failed=baseline.run_child([sys.executable,"-u","-c",program],Path(directory),
                      config,"fake-key",report,writer,pilot_only=True,
                      pilot_case_id=baseline.DISTRACTOR_PILOT_CASE_ID)
            self.assertEqual(failed["launcherFailure"]["failureCode"],"execution_mode_mismatch")
            self.assertNotEqual(failed["status"],"pilot_sample_passed")

    def test_tool_probe_without_reasoning_has_narrower_coverage_than_encrypted_replay(self):
        base = {
            "status": "passed", "stage": "completed",
            "modelCalls": 2, "httpRequests": 2,
            "reasoningSeen": False, "encryptedReplaySeen": False,
            "usage": fixture.usage(2),
        }
        baseline.validate_tool_probe(base)
        note = baseline.tool_probe_scope_note(base)
        self.assertIn("普通工具续接已通过", note)
        self.assertIn("没有实际覆盖加密 reasoning 回放", note)
        report = pilot_report()
        report["preflight"]["toolProbe"] = base
        self.assertIn(note, baseline.render_markdown(report))
        encrypted = dict(base, reasoningSeen=True, encryptedReplaySeen=True)
        baseline.validate_tool_probe(encrypted)
        self.assertIsNone(baseline.tool_probe_scope_note(encrypted))
        missing = dict(base, reasoningSeen=True)
        with self.assertRaisesRegex(baseline.ProtocolFailure, "invalid_tool_probe"):
            baseline.validate_tool_probe(missing)

    def test_context_budget_diagnosis_distinguishes_host_service_ceiling_from_model_capacity(self):
        guidance = baseline.failure_advice("evidence", "context_budget_exceeded", {})
        for phrase in ("ContextPolicy", "maxInputTokens", "contextTokens", "TokenCounter", "加密 reasoning", "不要"):
            self.assertIn(phrase, guidance)
        self.assertNotIn("核对 endpoint、model ID", guidance)

    def test_only_one_sample_and_unmodified_quality_dataset_identity(self):
        config = quality.quality_config()
        report = pilot_report()
        baseline.validate_report(report)
        self.assertEqual(report["status"], "pilot_sample_passed")
        self.assertEqual(report["runner"]["pilotCaseID"], "code-01")
        self.assertEqual(report["runner"]["taskVersion"], "release-evidence-quality-v1")
        self.assertEqual(report["summary"]["requested"], 40)
        self.assertEqual(report["summary"]["attempted"], 1)
        self.assertEqual(report["summary"]["skipped"], 39)
        self.assertFalse(report["summary"]["complete"])
        self.assertEqual(report["samples"][0]["caseID"], "code-01")
        self.assertEqual(report["samples"][0]["sampleID"], "code-01-01")
        markdown = baseline.render_markdown(report)
        self.assertIn("固定单案例真实任务试运行", markdown)
        self.assertIn("不能给出 40 案例成功率", markdown)
        self.assertNotIn("Evidence-quality 分组观测", markdown)
        comparable, complete = baseline.comparison_markdown(report, quality.quality_report())
        self.assertFalse(complete)
        self.assertIn("pilotOnly", comparable)

    def test_failure_and_preflight_failure_never_become_quality_success(self):
        failed = pilot_report(passed=False)
        self.assertEqual(failed["status"], "pilot_sample_failed")
        self.assertEqual(failed["summary"]["attempted"], 1)
        self.assertEqual(failed["summary"]["failed"], 1)
        self.assertEqual(failed["summary"]["skipped"], 39)
        baseline.validate_report(failed)
        preflight_failed = pilot_report(preflight_passed=False)
        self.assertEqual(preflight_failed["status"], "failed")
        self.assertEqual(preflight_failed["summary"]["attempted"], 0)
        self.assertEqual(preflight_failed["summary"]["skipped"], 40)
        baseline.validate_report(preflight_failed)
        for entry in (failed, preflight_failed):
            forged = copy.deepcopy(entry)
            forged["status"] = "passed"
            with self.assertRaisesRegex(baseline.SafeFailure, "report_status_mismatch"):
                baseline.validate_report(forged)

    def test_pilot_rejects_forged_case_sample_count_and_complete_summary(self):
        config = quality.quality_config()
        original = pilot_events(config)
        for mutation in (
            lambda start: start.update(pilotOnly=False),
            lambda start: start.update(pilotCaseID="window-01"),
            lambda start: start.pop("pilotCaseID"),
            lambda start: start.update(preflightOnly=True),
        ):
            with self.subTest(start_mutation=mutation):
                report = baseline.new_report(config, {})
                start = copy.deepcopy(original[0])
                mutation(start)
                with self.assertRaises(baseline.ProtocolFailure):
                    baseline.accept_event(report, start)
        for mutation in (
            lambda sample: sample.update(caseID="window-01"),
            lambda sample: sample.update(sampleID="forged-1"),
            lambda sample: sample.update(repetition=2),
        ):
            with self.subTest(sample_mutation=mutation):
                report = baseline.new_report(config, {})
                for event in original[:2]:
                    baseline.accept_event(report, event)
                bad = copy.deepcopy(original[2])
                mutation(bad["sample"])
                with self.assertRaises(baseline.ProtocolFailure):
                    baseline.accept_event(report, bad)
                self.assertEqual(report["samples"], [])
        report = baseline.new_report(config, {})
        for event in original[:3]:
            baseline.accept_event(report, event)
        extra = copy.deepcopy(original[2])
        extra["sample"]["caseID"] = "window-01"
        extra["sample"]["sampleID"] = "window-01-01"
        with self.assertRaisesRegex(baseline.ProtocolFailure, "pilot_sample_conflict"):
            baseline.accept_event(report, extra)
        for mutation in (
            lambda summary: summary.update(complete=True),
            lambda summary: summary.update(attempted=40, passed=40, skipped=0),
            lambda summary: summary.update(requested=1, skipped=0),
            lambda summary: summary.update(failed=1, passed=0),
        ):
            with self.subTest(summary_mutation=mutation):
                candidate = copy.deepcopy(original[3])
                mutation(candidate["summary"])
                with self.assertRaises(baseline.ProtocolFailure):
                    baseline.accept_event(report, candidate)
        self.assertIsNone(report["summary"])

    def test_runner_enforces_pilot_mode_and_writes_private_report(self):
        config = quality.quality_config()
        with tempfile.TemporaryDirectory() as directory:
            writer = baseline.ReportWriter(Path(directory) / "reports")
            report = baseline.new_report(config, {})
            program = fixture.child_program(pilot_events(config), exit_code=0)
            with contextlib.redirect_stdout(io.StringIO()):
                observed = baseline.run_child(
                    [sys.executable, "-u", "-c", program], Path(directory), config,
                    "private-test-key-never-store", report, writer, pilot_only=True)
            self.assertEqual(observed["status"], "pilot_sample_passed")
            self.assertEqual(len(observed["samples"]), 1)
            self.assertEqual(baseline.read_report(writer.json_path)["status"], "pilot_sample_passed")
            self.assertNotIn("private-test-key-never-store", writer.json_path.read_text())
            self.assertNotIn("private-test-key-never-store", writer.md_path.read_text())
            with contextlib.redirect_stderr(io.StringIO()) as output, \
                    mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "launch") as refused:
                self.assertEqual(baseline.main(["retry", str(writer.json_path)]), 2)
                refused.assert_not_called()
                self.assertIn("pilot_report_requires_explicit_quality", output.getvalue())

    def test_pilot_parent_rejects_child_mode_mismatch(self):
        config = quality.quality_config()
        for declared, expected in ((True, False), (False, True)):
            with self.subTest(declared=declared), tempfile.TemporaryDirectory() as directory:
                writer = baseline.ReportWriter(Path(directory) / "reports")
                report = baseline.new_report(config, {})
                events = pilot_events(config)
                if not declared:
                    del events[0]["pilotOnly"]
                    del events[0]["pilotCaseID"]
                program = fixture.child_program(events, exit_code=0)
                with contextlib.redirect_stdout(io.StringIO()):
                    observed = baseline.run_child(
                        [sys.executable, "-u", "-c", program], Path(directory),
                        config, "fake-test-key", report, writer, pilot_only=expected)
                self.assertEqual(observed["launcherFailure"]["failureCode"],
                                 "execution_mode_mismatch")
                self.assertNotEqual(observed["status"], "pilot_sample_passed")
                self.assertEqual(observed["samples"], [])
                baseline.read_report(writer.json_path)

    def test_pilot_parent_stops_after_first_sample_even_if_child_claims_another(self):
        config = quality.quality_config()
        records = pilot_events(config)
        extra = copy.deepcopy(records[2])
        extra["sample"]["caseID"] = "window-01"
        extra["sample"]["sampleID"] = "window-01-01"
        records.insert(3, extra)
        with tempfile.TemporaryDirectory() as directory:
            writer = baseline.ReportWriter(Path(directory) / "reports")
            report = baseline.new_report(config, {})
            program = fixture.child_program(records, exit_code=0)
            with contextlib.redirect_stdout(io.StringIO()):
                result = baseline.run_child(
                    [sys.executable, "-u", "-c", program], Path(directory),
                    config, "fake-test-key", report, writer, pilot_only=True)
            self.assertEqual(result["launcherFailure"]["failureCode"],
                             "pilot_sample_conflict")
            self.assertEqual(len(result["samples"]), 1)
            self.assertNotEqual(result["status"], "pilot_sample_passed")
            baseline.read_report(writer.json_path)

    def test_pilot_cli_requires_tty_and_single_quality_repetition(self):
        original = quality.quality_report()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "quality.json"
            path.write_text(json.dumps(original), encoding="utf-8")
            with mock.patch.object(sys.stdin, "isatty", return_value=False), \
                    mock.patch.object(baseline, "launch") as denied, \
                    contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(baseline.main(["pilot", str(path)]), 2)
                denied.assert_not_called()
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "repository_root", return_value=Path(directory)), \
                    mock.patch.object(baseline, "launch", return_value=0) as launched, \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(baseline.main(["pilot", str(path)]), 0)
                self.assertTrue(launched.call_args.kwargs["pilot_only"])
                self.assertIn("最多启动 1 次真实 Agent 任务", output.getvalue())
            for bad in (
                quality.quality_report(quality.quality_config(3)),
                fixture.complete_report(),
            ):
                path.write_text(json.dumps(bad), encoding="utf-8")
                with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                        mock.patch.object(sys.stderr, "isatty", return_value=True), \
                        mock.patch.object(baseline, "launch") as refused, \
                        contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(baseline.main(["pilot", str(path)]), 2)
                    refused.assert_not_called()
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "collect_configuration",
                                      return_value=quality.quality_config()) as collect, \
                    mock.patch.object(baseline, "launch", return_value=0) as launched:
                self.assertEqual(baseline.main(["pilot"]), 0)
                collect.assert_called_once_with(baseline.QUALITY_SUITE, pilot_only=True)
                self.assertTrue(launched.call_args.kwargs["pilot_only"])
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(baseline.main(["pilot", "a", "b"]), 2)

    def test_launch_cannot_start_full_task_or_gain_extra_silent_repetition(self):
        config = quality.quality_config()
        seen = []
        real = baseline.run_child
        def wrapped(command, root, selected, secret, report, writer, **options):
            self.assertEqual(command[1:], ["-mode=baseline-pilot-stdin"])
            self.assertTrue(options["pilot_only"])
            self.assertEqual(secret, "new-test-key")
            seen.append("run")
            program = fixture.child_program(pilot_events(selected), exit_code=0)
            return real([sys.executable, "-u", "-c", program], root, selected,
                        secret, report, writer, pilot_only=True)
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=wrapped), \
                contextlib.redirect_stdout(io.StringIO()):
            code = baseline.launch(config, Path(directory), get_key=lambda: (seen.append("key") or "new-test-key"),
                                   builder=lambda *_: seen.append("build"), pilot_only=True)
            self.assertEqual(code, 0)
            self.assertEqual(seen, ["build", "key", "run"])
            report_path = next((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            self.assertEqual(baseline.read_report(report_path)["status"], "pilot_sample_passed")
        with self.assertRaisesRegex(baseline.SafeFailure, "pilot_requires_one_quality_case"):
            baseline.launch(quality.quality_config(2), Path("/tmp"),
                            get_key=lambda: self.fail("key must not be read"),
                            builder=lambda *_: self.fail("builder must not run"),
                            pilot_only=True)
        with self.assertRaisesRegex(baseline.SafeFailure, "pilot_requires_one_quality_case"):
            baseline.launch(fixture.configuration(), Path("/tmp"), pilot_only=True)


if __name__ == "__main__":
    unittest.main()
