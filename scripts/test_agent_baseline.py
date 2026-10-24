"""Offline tests for the standard-library launcher; no keys or network required."""

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import warnings

SCRIPT = Path(__file__).with_name("run-agent-baseline.py")
SPEC = importlib.util.spec_from_file_location("agent_baseline_launcher", SCRIPT)
baseline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(baseline)


def configuration():
    return {
        "provider": "openai", "endpoint": "https://gateway.example/custom/infer",
        "model": "fixture-model", "contextTokens": 8192, "maxOutputTokens": 1024,
        "repetitions": 1, "allowUnknownStructuredModel": False,
    }


def start_event(config=None):
    config = config or configuration()
    public = {name: value for name, value in config.items() if name != "endpoint"}
    public.update(endpointOrigin="https://gateway.example", endpointHash="sha256:" + "e" * 64)
    return {
        "type": "start", "schemaVersion": 1, "taskVersion": "release-recovery-v1",
        "datasetHash": "sha256:" + "d" * 64,
        "profile": {
            "sampling": "sequential-round-robin",
            "contextPolicy": {"maxInputTokens": 6144},
            "oracleVersion": "evidence-draft-recovery-v1",
        },
        "configuration": public, "requestedSamples": 2 * config["repetitions"],
    }


def usage(missing=4):
    return {"knownCalls": 0, "missingCalls": missing, "complete": missing == 0, "known": None}


def sample(index=1, passed=True):
    result = {
        "sampleID": "sample-%d" % index, "caseID": "case-%d" % index, "repetition": 1,
        "status": "passed" if passed else "failed", "stage": "completed" if passed else "oracle",
        "durationMs": 10 * index, "modelCalls": 4, "toolCalls": 3,
        "oraclePassed": passed, "recoveryChecks": dict.fromkeys(baseline.RECOVERY_FIELDS, passed),
        "usage": usage(), "lookupCalls": 1, "artifactReads": 1, "draftWrites": 1,
        "modelDurationsMs": [1, 2, 3, 4],
        "toolExecutions": [{"toolKey": "fixture.draft", "callIDHash": "sha256:" + "a" * 64, "executions": 1}],
    }
    if not passed:
        result["failureCode"] = "oracle_failed"
    return result


def events(passed=True, preflight_passed=True):
    preflight = {
        "type": "preflight", "status": "passed" if preflight_passed else "failed",
        "durationMs": 5, "usage": None,
    }
    if not preflight_passed:
        preflight["failureCode"] = "preflight_authentication"
    samples = [sample(1, passed), sample(2, passed)] if preflight_passed else []
    count = len(samples)
    summary = {
        "requested": 2, "attempted": count, "passed": count if passed else 0,
        "failed": 0 if passed else count, "skipped": 2 - count,
        "successRate": (1.0 if passed else 0.0) if count else None,
        "complete": count == 2,
        "durationMs": {"count": count, "total": 30 if count else 0, "min": 10 if count else 0,
                       "max": 20 if count else 0, "p50": 10 if count else 0, "p95": 20 if count else 0},
        "usage": usage(4 * count),
    }
    return [start_event(), preflight] + [
        {"type": "sample", "sample": value} for value in samples
    ] + [{"type": "summary", "summary": summary}]


def complete_report(passed=True):
    report = baseline.new_report(configuration(), {"revision": "a" * 40, "dirty": False})
    for event in events(passed):
        baseline.accept_event(report, event)
    baseline.finish_report(report, 0 if passed else 1)
    return report


def child_program(records, exit_code=0, ending="", echo_secret=False):
    # This executable source contains no fake key. The key enters via stdin.
    return (
        "import json, os, sys, time\n"
        "config = json.loads(sys.stdin.readline())\n"
        "assert 'apiKey' in config\n"
        "assert config['apiKey'] not in '\\x00'.join(sys.argv)\n"
        "assert 'OPENAI_API_KEY' not in os.environ\n"
        "assert 'ANTHROPIC_API_KEY' not in os.environ\n"
        "records = json.loads(" + repr(json.dumps(records)) + ")\n"
        + ("records[2]['sample']['sampleID'] = config['apiKey']\n"
           "sys.stderr.write(config['apiKey'])\n" if echo_secret else "")
        + "for record in records:\n"
        "    print(json.dumps(record), flush=True)\n"
        + ending
        + "\nsys.exit(" + str(exit_code) + ")\n"
    )


class EndpointTests(unittest.TestCase):
    def test_complete_endpoints_and_no_implicit_path_changes(self):
        for provider, endpoint in [
            ("openai", ""), ("anthropic", ""), ("openai", "https://example.com"),
            ("openai", "https://gateway.example/proxy/v1/responses"),
            ("anthropic", "https://gateway.example/custom/infer"),
            ("openai", "http://localhost:8000/responses"),
            ("openai", "http://127.0.0.2:8000/responses"),
            ("anthropic", "http://[::1]:8000/messages"),
            ("openai", "http://[::ffff:127.0.0.1]:8000/responses"),
        ]:
            with self.subTest(provider=provider, endpoint=endpoint):
                expected = endpoint or baseline.DEFAULT_ENDPOINTS[provider]
                self.assertEqual(baseline.validate_endpoint(provider, endpoint), expected)

    def test_rejects_credentials_queries_controls_and_protocol_mismatch(self):
        for provider, endpoint in [
            ("openai", "https://user:password@example.com/v1/responses"),
            ("openai", "https://@example.com/custom"),
            ("openai", "https://example.com/v1/responses?key=x"),
            ("openai", "https://example.com/v1/responses?"),
            ("openai", "https://example.com/v1/responses#fragment"),
            ("openai", "https://example.com/v1/responses#"),
            ("openai", "\thttps://example.com/responses"),
            ("openai", "https://exa\nmple.com/responses"),
            ("openai", "https://example.com/\x7fresponses"),
            ("openai", "https://example.com/a b"),
            ("openai", "http://example.com/responses"),
            ("openai", "http://localhost.evil.example/responses"),
            ("openai", "http://10.0.0.1/responses"),
            ("openai", "http://LOCALHOST:8000/responses"),
            ("openai", "https://example.com/v1/chat/completions"),
            ("openai", "https://example.com/v1/messages"),
            ("anthropic", "https://example.com/v1/responses/"),
            ("openai", "file:///tmp/provider"),
            ("openai", "example.com/v1/responses"),
            ("openai", "https://example.com:99999/responses"),
        ]:
            with self.subTest(provider=provider, endpoint=endpoint):
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_endpoint(provider, endpoint)

    def test_explicit_capacity_and_bounded_repetitions(self):
        for field, value in [
            ("contextTokens", 0), ("contextTokens", 4096), ("contextTokens", True),
            ("repetitions", 0), ("repetitions", 21), ("maxOutputTokens", 0),
            ("allowUnknownStructuredModel", "yes"),
        ]:
            config = configuration()
            config[field] = value
            with self.subTest(field=field, value=value):
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_configuration(config)


class SecretAndBuildTests(unittest.TestCase):
    def test_build_failure_never_requests_key_and_saves_failure(self):
        reader = mock.Mock(side_effect=AssertionError("key must not be requested"))
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
                baseline, "source_metadata", return_value={"revision": None, "dirty": None}):
            with contextlib.redirect_stdout(io.StringIO()):
                result = baseline.launch(
                    configuration(), Path(directory), get_key=reader,
                    builder=mock.Mock(side_effect=baseline.SafeFailure("build_failed")),
                )
            self.assertEqual(result, 1)
            reader.assert_not_called()
            reports = list((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            self.assertEqual(len(reports), 1)
            saved = baseline.read_report(reports[0])
            self.assertEqual(saved["status"], "failed")
            self.assertEqual(saved["launcherFailure"]["failureCode"], "build_failed")

    def test_getpass_requires_tty_and_never_falls_back_to_echo(self):
        with mock.patch.object(sys.stdin, "isatty", return_value=False):
            with self.assertRaisesRegex(baseline.SafeFailure, "interactive_tty_required"):
                baseline.read_api_key()
        with mock.patch.object(sys.stdin, "isatty", return_value=True), mock.patch.object(
                sys.stderr, "isatty", return_value=True):
            with mock.patch.object(baseline.getpass, "getpass", return_value="  fake-key  "):
                self.assertEqual(baseline.read_api_key(), "fake-key")
            with mock.patch.object(
                    baseline.getpass, "getpass",
                    side_effect=lambda *_: warnings.warn("unsafe echo", baseline.getpass.GetPassWarning)):
                with self.assertRaisesRegex(baseline.SafeFailure, "hidden_input_unavailable"):
                    baseline.read_api_key()

    def test_help_does_not_require_a_terminal_or_build(self):
        with mock.patch.object(sys.stdin, "isatty", side_effect=AssertionError("TTY not needed")), \
                mock.patch.object(baseline, "build_runner") as builder, \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(baseline.main(["--help"]), 0)
            self.assertIn("compare", output.getvalue())
            builder.assert_not_called()


class ProcessReportTests(unittest.TestCase):
    secret = "fake-live-key-never-store"

    def invoke(self, directory, records, exit_code=0, ending="", echo_secret=False, timeout=None):
        writer = baseline.ReportWriter(Path(directory) / "reports")
        report = baseline.new_report(configuration(), {"revision": "a" * 40, "dirty": False})
        program = child_program(records, exit_code, ending, echo_secret)
        command = [sys.executable, "-u", "-c", program, "-mode=baseline-stdin"]
        with contextlib.redirect_stdout(io.StringIO()) as output:
            result = baseline.run_child(
                command, Path(directory), configuration(), self.secret, report, writer,
                timeout_seconds=timeout,
            )
        return result, writer, command, output.getvalue()

    def test_secret_only_stdin_redaction_and_private_report_modes(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(
                os.environ, {"OPENAI_API_KEY": "inherited-openai", "ANTHROPIC_API_KEY": "inherited-anthropic"}):
            real_popen = baseline.subprocess.Popen
            calls = []

            def capture(*args, **kwargs):
                calls.append((args, kwargs))
                return real_popen(*args, **kwargs)

            with mock.patch.object(baseline.subprocess, "Popen", side_effect=capture):
                result, writer, command, output = self.invoke(directory, events(), echo_secret=True)
            self.assertEqual(result["status"], "passed")
            self.assertNotIn(self.secret, json.dumps(result))
            self.assertEqual(result["samples"][0]["sampleID"], "[REDACTED]")
            self.assertNotIn(self.secret, "\x00".join(command))
            self.assertNotIn(self.secret, output)
            self.assertNotIn("OPENAI_API_KEY", calls[0][1]["env"])
            self.assertNotIn("ANTHROPIC_API_KEY", calls[0][1]["env"])
            self.assertNotIn("PWD", calls[0][1]["env"])
            self.assertEqual(calls[0][1]["stderr"], subprocess.DEVNULL)
            self.assertEqual(calls[0][1]["cwd"], Path(directory))
            self.assertEqual(calls[0][1]["env"]["GOWORK"], str(Path(directory).resolve() / "go.work"))
            for path in (writer.json_path, writer.md_path):
                self.assertNotIn(self.secret, path.read_text(encoding="utf-8"))
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 2)
            self.assertIn("toolExecutions", result["samples"][0])
            self.assertIn("modelDurationsMs", result["samples"][0])

    def test_completed_failed_samples_are_preserved_with_zero_success_rate(self):
        with tempfile.TemporaryDirectory() as directory:
            result, writer, _, _ = self.invoke(directory, events(passed=False), exit_code=1)
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["summary"]["attempted"], 2)
            self.assertEqual(result["summary"]["successRate"], 0)
            self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 2)

    def test_preflight_failure_is_known_failure_not_a_zero_usage_success(self):
        with tempfile.TemporaryDirectory() as directory:
            result, writer, _, _ = self.invoke(directory, events(preflight_passed=False), exit_code=1)
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["samples"], [])
            self.assertIsNone(result["summary"]["successRate"])
            self.assertIsNone(result["summary"]["usage"]["known"])
            self.assertEqual(result["summary"]["skipped"], 2)
            baseline.read_report(writer.json_path)

    def test_invalid_jsonl_keeps_prior_sample_and_hides_raw_output(self):
        ending = "print('not-json-' + config['apiKey'], flush=True)\n"
        with tempfile.TemporaryDirectory() as directory:
            result, writer, _, output = self.invoke(directory, events()[:3], ending=ending)
            self.assertEqual(result["status"], "incomplete")
            self.assertEqual(result["launcherFailure"]["failureCode"], "invalid_jsonl")
            self.assertEqual(len(result["samples"]), 1)
            self.assertNotIn(self.secret, output + writer.json_path.read_text())
            self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 1)

    def test_nonzero_crash_and_timeout_keep_partial_reports(self):
        for exit_code, ending, timeout, failure in [
            (9, "", None, "missing_summary"),
            (0, "time.sleep(10)\n", 0.3, "runner_timeout"),
        ]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                result, writer, _, _ = self.invoke(directory, events()[:3], exit_code, ending, timeout=timeout)
                self.assertEqual(result["status"], "incomplete")
                self.assertEqual(result["launcherFailure"]["failureCode"], failure)
                self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 1)

    def test_keyboard_interrupt_reaps_child_and_keeps_checkpoint(self):
        original_get = baseline.queue.Queue.get
        received = [0]

        def interrupted_get(instance, *args, **kwargs):
            received[0] += 1
            if received[0] == 4:
                raise KeyboardInterrupt()
            return original_get(instance, *args, **kwargs)

        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
                baseline.queue.Queue, "get", new=interrupted_get):
            result, writer, _, _ = self.invoke(directory, events()[:3], ending="time.sleep(10)\n")
            self.assertEqual(result["status"], "interrupted")
            self.assertIsNotNone(result["runnerExitCode"])
            self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 1)

    def test_unknown_event_and_bounded_output_are_protocol_failures(self):
        for records, ending, expected in [
            ([{"type": "future-event"}], "", "unknown_event"),
            ([], "print('x' * (256 * 1024 + 1), flush=True)\n", "output_limit"),
        ]:
            with self.subTest(expected=expected), tempfile.TemporaryDirectory() as directory:
                result, _, _, _ = self.invoke(directory, records, ending=ending)
                self.assertEqual(result["status"], "incomplete")
                self.assertEqual(result["launcherFailure"]["failureCode"], expected)

    def test_write_failure_has_a_fixed_safe_error_and_stops_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            blocked = Path(directory) / "not-a-directory"
            blocked.write_text("fixture", encoding="utf-8")
            writer = baseline.ReportWriter(blocked)
            report = baseline.new_report(configuration(), {})
            with self.assertRaisesRegex(baseline.ReportFailure, "^report_write_failed$"):
                writer.save(report, self.secret)
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline.ReportWriter, "save", side_effect=baseline.ReportFailure("report_write_failed")), \
                contextlib.redirect_stdout(io.StringIO()) as out, \
                contextlib.redirect_stderr(io.StringIO()) as err:
            code = baseline.launch(configuration(), directory, get_key=lambda: self.secret, builder=lambda *_: None)
            self.assertEqual(code, 1)
            self.assertNotIn(self.secret, out.getvalue() + err.getvalue())
            self.assertIn("报告无法保存", err.getvalue())


class ComparisonTests(unittest.TestCase):
    def test_model_provider_revision_differences_are_visible_and_comparable(self):
        old = complete_report()
        new = copy.deepcopy(old)
        for configuration in (new["configuration"], new["runner"]["configuration"]):
            configuration.update(provider="anthropic", model="candidate-model")
        new["configuration"]["endpoint"] = "https://other.example/custom"
        new["source"]["revision"] = "b" * 40
        rendered, complete = baseline.comparison_markdown(old, new)
        self.assertTrue(complete)
        self.assertIn("candidate-model", rendered)
        self.assertIn("anthropic", rendered)
        self.assertIn("b" * 40, rendered)
        self.assertIn("attempted", rendered)
        self.assertIn("没有统计显著性或自动胜者", rendered)

    def test_dataset_profile_and_output_mismatch_reject_numeric_comparison(self):
        for field in ("datasetHash", "profile", "maxOutputTokens"):
            old = complete_report()
            new = copy.deepcopy(old)
            if field == "maxOutputTokens":
                new["configuration"][field] = new["runner"]["configuration"][field] = 512
            else:
                new["runner"][field] = "other" if field == "datasetHash" else {"contextPolicy": {"maxInputTokens": 10000}}
            with self.subTest(field=field):
                rendered, complete = baseline.comparison_markdown(old, new)
                self.assertFalse(complete)
                self.assertIn("无法进行数值比较", rendered)
                self.assertNotIn("## 已观察结果", rendered)

    def test_different_context_capacity_warns_with_matching_service_profile(self):
        old = complete_report()
        new = copy.deepcopy(old)
        new["configuration"]["contextTokens"] = new["runner"]["configuration"]["contextTokens"] = 16384
        rendered, complete = baseline.comparison_markdown(old, new)
        self.assertTrue(complete)
        self.assertIn("context capacity 不同", rendered)

    def test_incomplete_report_only_shows_observations(self):
        partial = baseline.new_report(configuration(), {"revision": "a" * 40, "dirty": False})
        for event in events()[:3]:
            baseline.accept_event(partial, event)
        baseline.finish_report(partial, 9)
        rendered, complete = baseline.comparison_markdown(complete_report(), partial)
        self.assertFalse(complete)
        self.assertIn("只能", rendered.replace("只列", "只能"))
        self.assertIn("不能给出总体提升", rendered)

    def test_unknown_schema_and_corrupt_counts_types_are_rejected(self):
        mutations = [
            lambda item: item.update(schemaVersion=2),
            lambda item: item.update(source=[]),
            lambda item: item.update(summary="invalid"),
            lambda item: item["summary"].update(passed=99),
            lambda item: item["summary"].update(successRate=0.7),
            lambda item: item["samples"][1].update(caseID="case-1"),
            lambda item: item["samples"][0].update(recoveryChecks={"sameRun": True}),
            lambda item: item["samples"][0].update(stage=[]),
        ]
        for mutate in mutations:
            report = complete_report()
            mutate(report)
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "corrupt.json"
                path.write_text(json.dumps(report), encoding="utf-8")
                with self.assertRaises(baseline.SafeFailure):
                    baseline.read_report(path)

    def test_partial_cannot_claim_a_complete_summary(self):
        report = baseline.new_report(configuration(), {})
        for event in events()[:3]:
            baseline.accept_event(report, event)
        summary = copy.deepcopy(events()[-1])
        summary["summary"].update(attempted=1, passed=1, skipped=1, complete=True)
        summary["summary"]["durationMs"]["count"] = 1
        with self.assertRaisesRegex(baseline.ProtocolFailure, "partial_marked_complete"):
            baseline.accept_event(report, summary)


if __name__ == "__main__":
    unittest.main()
