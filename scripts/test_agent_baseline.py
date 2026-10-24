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
        "model": "fixture-model", "contextTokens": 16384, "maxOutputTokens": 1024,
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
            "contextPolicy": {"maxInputTokens": 8192},
            "oracleVersion": "evidence-draft-recovery-v1",
        },
        "configuration": public, "requestedSamples": 2 * config["repetitions"],
    }


def usage(missing=4):
    return {"knownCalls": 0, "missingCalls": missing, "complete": missing == 0, "known": None}


def schema_check_fixture(kind="object", ok_type="boolean", ok_true=False):
    check = {
        "contentPresent": True, "jsonValid": kind != "invalid", "jsonType": kind,
        "schemaValid": False, "toolCallCount": 0, "hostedToolCallCount": 0,
        "artifactCount": 0, "continue": False,
    }
    if kind == "object":
        check.update(okPresent=ok_type != "missing", okType=ok_type, extraPropertyCount=0)
        if ok_type == "boolean":
            check["okIsTrue"] = ok_true
    return check


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
        preflight["failureCode"] = "provider_failed"
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

    def test_interactive_standard_base_paths_only_are_expanded(self):
        for provider, suffix in (("openai", "responses"), ("anthropic", "messages")):
            for entered, expected in (
                ("", baseline.DEFAULT_ENDPOINTS[provider]),
                ("https://gateway.example", "https://gateway.example/v1/" + suffix),
                ("https://gateway.example/", "https://gateway.example/v1/" + suffix),
                ("https://gateway.example/v1", "https://gateway.example/v1/" + suffix),
                ("https://gateway.example/proxy/v1/", "https://gateway.example/proxy/v1/" + suffix),
                ("http://127.0.0.1:8000/v1", "http://127.0.0.1:8000/v1/" + suffix),
                ("https://gateway.example/proxy/v1/" + suffix, "https://gateway.example/proxy/v1/" + suffix),
                ("https://gateway.example/custom/infer/", "https://gateway.example/custom/infer/"),
                ("https://gateway.example/custom", "https://gateway.example/custom"),
                ("https://gateway.example/v10", "https://gateway.example/v10"),
            ):
                with self.subTest(provider=provider, entered=entered):
                    self.assertEqual(baseline.resolve_interactive_endpoint(provider, entered), expected)

    def test_interactive_address_and_output_budget_are_shown_before_launch(self):
        entered = "https://gateway.example"
        final = entered + "/v1/responses"
        choices = ["1", entered, "fixture-model", "16384", "", "1", "n"]
        with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                mock.patch.object(sys.stderr, "isatty", return_value=True), \
                mock.patch("builtins.input", side_effect=choices), \
                mock.patch.object(baseline, "read_api_key") as key_reader, \
                contextlib.redirect_stdout(io.StringIO()) as output:

            def launched(config, _root):
                self.assertEqual(config["endpoint"], final)
                self.assertEqual(config["maxOutputTokens"], 1024)
                self.assertIn(entered + " → " + final, output.getvalue())
                self.assertIn("本次任务输出预算默认 1024，不必填模型最大输出容量", output.getvalue())
                key_reader.assert_not_called()
                return 0

            with mock.patch.object(baseline, "launch", side_effect=launched):
                self.assertEqual(baseline.main([]), 0)

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
                with self.assertRaises(baseline.SafeFailure):
                    baseline.resolve_interactive_endpoint(provider, endpoint)

    def test_explicit_capacity_and_bounded_repetitions(self):
        for field, value in [
            ("contextTokens", 0), ("contextTokens", 4096), ("contextTokens", 8192), ("contextTokens", True),
            ("repetitions", 0), ("repetitions", 21), ("maxOutputTokens", 0),
            ("allowUnknownStructuredModel", "yes"),
        ]:
            config = configuration()
            config[field] = value
            with self.subTest(field=field, value=value):
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_configuration(config)


    def test_current_input_requires_the_current_service_budget(self):
        self.assertEqual(baseline.SERVICE_INPUT_TOKENS, 8192)
        config = configuration()
        config["contextTokens"] = baseline.SERVICE_INPUT_TOKENS + config["maxOutputTokens"]
        self.assertEqual(baseline.validate_configuration(config), config)
        config["contextTokens"] -= 1
        with self.assertRaisesRegex(baseline.SafeFailure, "^insufficient_context_capacity$"):
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
            result, writer, _, output = self.invoke(directory, events(passed=False), exit_code=1)
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["summary"]["attempted"], 2)
            self.assertEqual(result["summary"]["successRate"], 0)
            self.assertEqual(len(baseline.read_report(writer.json_path)["samples"]), 2)
            self.assertIn("失败分类 oracle_failed", output)
            markdown = writer.md_path.read_text(encoding="utf-8")
            self.assertIn("阶段 oracle；失败分类 oracle_failed", markdown)
            self.assertIn("已尝试 2，跳过 0", markdown)
            self.assertNotIn("任务尚未开始", markdown)

    def test_preflight_failure_is_known_failure_not_a_zero_usage_success(self):
        with tempfile.TemporaryDirectory() as directory:
            result, writer, _, _ = self.invoke(directory, events(preflight_passed=False), exit_code=1)
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["samples"], [])
            self.assertIsNone(result["summary"]["successRate"])
            self.assertIsNone(result["summary"]["usage"]["known"])
            self.assertEqual(result["summary"]["skipped"], 2)
            baseline.read_report(writer.json_path)

    def test_launch_explains_preflight_failure_in_console_and_markdown(self):
        real_run_child = baseline.run_child

        def fixture_runner(_command, root, config, secret, report, writer):
            program = child_program(events(preflight_passed=False), exit_code=1)
            return real_run_child([sys.executable, "-u", "-c", program], root, config, secret, report, writer)

        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(baseline.launch(
                configuration(), Path(directory), get_key=lambda: self.secret, builder=lambda *_: None,
            ), 1)
            markdown_path = next((Path(directory) / "artifacts" / "agent-baselines").glob("*.md"))
            for rendered in (output.getvalue(), markdown_path.read_text(encoding="utf-8")):
                self.assertIn("阶段 preflight；失败分类 provider_failed", rendered)
                self.assertIn("已尝试 0，跳过 2", rendered)
                self.assertIn("任务尚未开始", rendered)
                self.assertIn("下一步", rendered)
                self.assertIn("现有证据不足以确定具体原因", rendered)
                self.assertNotIn(self.secret, rendered)
                self.assertNotIn("HTTP 401", rendered)
            self.assertIn("预检：未通过", output.getvalue())

    def test_preflight_transport_facts_are_preserved_and_drive_bounded_advice(self):
        for status, kind, expected in (
            (200, "html", "响应声明为 HTML"),
            (302, "html", "最终 API 地址"),
            (400, "json", "输出预算"),
            (401, "json", "不足以确定密钥是否错误"),
            (403, "json", "网关路由和策略"),
            (404, "json", "完整 API 地址"),
            (422, "json", "输出预算"),
            (429, "json", "配额"),
            (660, "other", "现有证据不足以确定具体原因"),
        ):
            with self.subTest(status=status):
                records = events(preflight_passed=False)
                records[1].update(httpStatus=status, responseKind=kind)
                report = baseline.new_report(configuration(), {})
                for event in records:
                    baseline.accept_event(report, event)
                baseline.finish_report(report, 1)
                baseline.validate_report(report)
                self.assertEqual(report["preflight"]["httpStatus"], status)
                self.assertEqual(report["preflight"]["responseKind"], kind)
                rendered = baseline.render_markdown(report)
                self.assertIn("HTTP %d" % status, rendered)
                self.assertIn(expected, rendered)

    def test_preflight_transport_rejects_invalid_or_unpaired_metadata(self):
        for changes in (
            {"httpStatus": 99, "responseKind": "json"},
            {"httpStatus": 1000, "responseKind": "json"},
            {"httpStatus": True, "responseKind": "json"},
            {"httpStatus": 200.0, "responseKind": "json"},
            {"httpStatus": 200, "responseKind": "raw provider payload"},
            {"httpStatus": 200}, {"responseKind": "html"},
        ):
            with self.subTest(changes=changes):
                report = baseline.new_report(configuration(), {})
                baseline.accept_event(report, start_event())
                preflight = events(preflight_passed=False)[1]
                preflight.update(changes)
                with self.assertRaisesRegex(baseline.ProtocolFailure, "^invalid_preflight_transport$"):
                    baseline.accept_event(report, preflight)

    def test_schema_check_is_saved_and_explained_in_console_and_markdown(self):
        records = events(preflight_passed=False)
        check = schema_check_fixture()
        records[1].update(failureCode="strict_schema_probe_failed", httpStatus=200,
                          responseKind="json", schemaCheck=check)
        real_run_child = baseline.run_child

        def fixture_runner(_command, root, config, secret, report, writer):
            program = child_program(records, exit_code=1)
            return real_run_child([sys.executable, "-u", "-c", program], root, config, secret, report, writer)

        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(baseline.launch(
                configuration(), Path(directory), get_key=lambda: self.secret, builder=lambda *_: None,
            ), 1)
            report_path = next((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            loaded = baseline.read_report(report_path)
            self.assertEqual(loaded["preflight"]["schemaCheck"], check)
            for rendered in (output.getvalue(), report_path.with_suffix(".md").read_text(encoding="utf-8")):
                self.assertIn("ok 为 false，要求 true", rendered)
                self.assertIn("已尝试 0，跳过 2", rendered)
                self.assertIn("不能定位网关内部哪一步失效", rendered)
                self.assertNotIn("每任务 1 次重新运行", rendered)
                self.assertNotIn(self.secret, rendered)

    def test_schema_check_reasons_distinguish_structure_and_behavior(self):
        for check, expected in (
            (dict(schema_check_fixture("invalid"), contentPresent=False), "回答内容为空"),
            (schema_check_fixture("invalid"), "不是可解析的单个 JSON 文档"),
            (schema_check_fixture("string"), "根类型为 string，要求 object"),
            (schema_check_fixture(ok_type="missing"), "必填字段 ok 缺失"),
            (schema_check_fixture(ok_type="string"), "ok 类型为 string，要求 boolean"),
            (dict(schema_check_fixture(ok_true=True), extraPropertyCount=2), "存在 2 个额外属性"),
            (dict(schema_check_fixture(ok_true=True), schemaValid=True, toolCallCount=1), "无工具调用"),
            ({**schema_check_fixture(ok_true=True), "schemaValid": True, "continue": True}, "continue=false"),
        ):
            with self.subTest(expected=expected):
                baseline.validate_schema_check(check)
                self.assertIn(expected, "\n".join(baseline.schema_diagnostics(check)))

    def test_invalid_schema_check_never_enters_saved_report(self):
        mutations = [
            lambda check: check.update(jsonType="untrusted-response-type"),
            lambda check: check.update(contentPresent=1),
            lambda check: check.update(jsonValid=False),
            lambda check: check.update(jsonType="array"),
            lambda check: check.update(okType="string"),
            lambda check: check.update(toolCallCount=True),
            lambda check: check.update(artifactCount=-1),
            lambda check: check.update(okType="untrusted-field-value"),
            lambda check: check.update(okPresent=False),
            lambda check: check.update(okIsTrue="true"),
            lambda check: check.pop("extraPropertyCount"),
        ]
        for mutate in mutations:
            check = schema_check_fixture()
            mutate(check)
            report = baseline.new_report(configuration(), {})
            baseline.accept_event(report, start_event())
            preflight = events(preflight_passed=False)[1]
            preflight.update(failureCode="strict_schema_probe_failed", schemaCheck=check)
            with self.subTest(mutate=mutate), self.assertRaisesRegex(
                    baseline.ProtocolFailure, "^invalid_preflight_schema_check$"):
                baseline.accept_event(report, preflight)
            self.assertIsNone(report["preflight"])
        for status, code in (("passed", None), ("failed", "provider_failed")):
            report = baseline.new_report(configuration(), {})
            baseline.accept_event(report, start_event())
            preflight = dict(events(preflight_passed=False)[1], status=status, schemaCheck=schema_check_fixture())
            if code is not None:
                preflight["failureCode"] = code
            else:
                preflight.pop("failureCode")
            with self.assertRaisesRegex(baseline.ProtocolFailure, "^invalid_preflight_schema_check$"):
                baseline.accept_event(report, preflight)
        records = events(preflight_passed=False)
        records[1].update(failureCode="strict_schema_probe_failed", schemaCheck=schema_check_fixture())
        records[1]["schemaCheck"][self.secret] = self.secret
        with tempfile.TemporaryDirectory() as directory:
            result, writer, _, output = self.invoke(directory, records, exit_code=1)
            self.assertEqual(result["launcherFailure"]["failureCode"], "invalid_preflight_schema_check")
            self.assertIsNone(result["preflight"])
            self.assertNotIn(self.secret, output + writer.json_path.read_text() + writer.md_path.read_text())

    def test_legacy_schema_failure_has_no_invented_structure_facts(self):
        records = events(preflight_passed=False)
        records[1]["failureCode"] = "strict_schema_probe_failed"
        with tempfile.TemporaryDirectory() as directory:
            _, writer, _, _ = self.invoke(directory, records, exit_code=1)
            report = baseline.read_report(writer.json_path)
            self.assertNotIn("schemaCheck", report["preflight"])
            rendered = writer.md_path.read_text(encoding="utf-8")
            self.assertIn("本报告未记录 schema 细项", rendered)
            self.assertNotIn("模型回答检查", rendered)
            self.assertNotIn("每任务 1 次重新运行", rendered)

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
        new["configuration"]["contextTokens"] = new["runner"]["configuration"]["contextTokens"] = 32768
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

    def test_historical_root_endpoint_is_read_and_compared_without_rewriting(self):
        config = configuration()
        config["endpoint"] = "https://gateway.example"
        report = baseline.new_report(config, {})
        records = events(preflight_passed=False)
        records[0] = start_event(config)
        for event in records:
            baseline.accept_event(report, event)
        baseline.finish_report(report, 1)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "historical.json"
            original = json.dumps(report).encode("utf-8")
            path.write_bytes(original)
            loaded = baseline.read_report(path)
            rendered, complete = baseline.comparison_markdown(loaded, loaded)
            self.assertFalse(complete)
            self.assertEqual(loaded["configuration"]["endpoint"], config["endpoint"])
            self.assertEqual(path.read_bytes(), original)
            self.assertIn("https://gateway.example", rendered)
            self.assertNotIn("https://gateway.example/v1/responses", rendered)
            self.assertIn("阶段 preflight；失败分类 provider_failed", rendered)
            self.assertIn("已尝试 0，跳过 2", rendered)

    def test_historical_service_budget_remains_readable_but_not_comparable_to_current(self):
        historical = complete_report()
        historical["configuration"]["contextTokens"] = historical["runner"]["configuration"]["contextTokens"] = 8192
        historical["runner"]["profile"]["contextPolicy"]["maxInputTokens"] = 6144
        historical["runner"]["datasetHash"] = "sha256:" + "f" * 64
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "historical-budget.json"
            original = json.dumps(historical).encode("utf-8")
            path.write_bytes(original)
            loaded = baseline.read_report(path)
            self.assertEqual(loaded["configuration"]["contextTokens"], 8192)
            self.assertEqual(loaded["runner"]["profile"]["contextPolicy"]["maxInputTokens"], 6144)
            with self.assertRaisesRegex(baseline.SafeFailure, "^insufficient_context_capacity$"):
                baseline.validate_configuration(loaded["configuration"])
            rendered, comparable = baseline.comparison_markdown(loaded, complete_report())
            self.assertFalse(comparable)
            self.assertIn("无法进行数值比较", rendered)
            self.assertIn("datasetHash", rendered)
            self.assertIn("profile", rendered)
            self.assertNotIn("## 已观察结果", rendered)
            self.assertEqual(path.read_bytes(), original)
            historical_only, comparable = baseline.comparison_markdown(loaded, loaded)
            self.assertTrue(comparable)
            self.assertIn("## 已观察结果", historical_only)

    def test_report_configuration_is_checked_against_its_recorded_budget(self):
        for service_cap in (8192, 0, -1, True, None, "6144"):
            report = complete_report()
            report["configuration"]["contextTokens"] = report["runner"]["configuration"]["contextTokens"] = 8192
            report["runner"]["profile"]["contextPolicy"]["maxInputTokens"] = service_cap
            with self.subTest(service_cap=service_cap), self.assertRaises(baseline.SafeFailure):
                baseline.validate_report(report)

    def test_historical_prestart_failure_has_no_invented_service_budget(self):
        config = configuration()
        config["contextTokens"] = 8192
        report = baseline.new_report(config, {})
        baseline.finish_report(report, None, {"stage": "launcher", "failureCode": "build_failed"})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "old-build-failure.json"
            original = json.dumps(report).encode("utf-8")
            path.write_bytes(original)
            loaded = baseline.read_report(path)
            self.assertEqual(loaded["status"], "failed")
            self.assertFalse(loaded["runner"])
            rendered, comparable = baseline.comparison_markdown(loaded, complete_report())
            self.assertFalse(comparable)
            self.assertIn("无法进行数值比较", rendered)
            self.assertEqual(path.read_bytes(), original)

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
