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


def wire_check_fixture(phase="final_answer", json_valid=False, schema_valid=False, matches=True):
    return {
        "request": {"capture": "complete", "format": {
            "type": "json_schema", "strictEnabled": True, "schemaMatchesProbe": True,
        }},
        "response": {
            "capture": "complete", "status": "completed", "format": {
                "type": "missing", "strictEnabled": False, "schemaMatchesProbe": False,
            },
            "outputItemCount": 1, "messageCount": 1, "messagesTruncated": False,
            "messages": [{
                "index": 0, "phase": phase, "textPartCount": 1, "refusalPartCount": 0,
                "unknownPartCount": 0, "jsonValidPartCount": int(json_valid),
                "schemaValidPartCount": int(schema_valid), "jsonValid": json_valid,
                "schemaValid": schema_valid, "matchesNormalized": matches,
                "leadingBOM": False, "codeFence": False,
            }],
        },
    }


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


class RetryTests(unittest.TestCase):
    def test_retry_reuses_public_configuration_builds_then_reads_fresh_key_and_creates_new_report(self):
        original = complete_report()
        original["configuration"]["apiKey"] = "old-fake-key-must-not-be-reused"
        steps = []
        fresh_key = "fresh-fake-key-only-stdin"
        real_run_child = baseline.run_child

        def fixture_runner(_command, root, config, secret, report, writer):
            self.assertEqual(set(config), set(baseline.PUBLIC_CONFIGURATION))
            self.assertEqual(config, configuration())
            self.assertEqual(secret, fresh_key)
            steps.append("run")
            return real_run_child([sys.executable, "-u", "-c", child_program(events())],
                                  root, config, secret, report, writer)

        def build(_root, _executable):
            steps.append("build")

        def key():
            steps.append("key")
            return fresh_key

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "old-report.json"
            original_bytes = json.dumps(original).encode("utf-8")
            path.write_bytes(original_bytes)
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "repository_root", return_value=Path(directory)), \
                    mock.patch.object(baseline, "source_metadata", return_value={}), \
                    mock.patch.object(baseline, "collect_configuration", side_effect=AssertionError("must reuse config")), \
                    mock.patch.object(baseline, "build_runner", side_effect=build), \
                    mock.patch.object(baseline, "read_api_key", side_effect=key), \
                    mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(baseline.main(["retry", str(path)]), 0)
            self.assertEqual(steps, ["build", "key", "run"])
            self.assertEqual(path.read_bytes(), original_bytes)
            self.assertIn("复用报告中的公开配置", output.getvalue())
            self.assertIn(configuration()["endpoint"], output.getvalue())
            self.assertIn("另加 1 次能力探测", output.getvalue())
            self.assertNotIn(original["configuration"]["apiKey"], output.getvalue())
            self.assertNotIn(fresh_key, output.getvalue())
            reports = list((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            self.assertEqual(len(reports), 1)
            saved = baseline.read_report(reports[0])
            self.assertEqual(saved["status"], "passed")
            self.assertEqual(saved["configuration"], configuration())
            self.assertNotIn(fresh_key, reports[0].read_text(encoding="utf-8"))
            self.assertNotIn(original["configuration"]["apiKey"], reports[0].read_text(encoding="utf-8"))

    def test_retry_preserves_full_endpoint_and_explicit_probe_choice(self):
        report = complete_report()
        report["configuration"]["endpoint"] = "https://gateway.example"
        report["configuration"]["allowUnknownStructuredModel"] = True
        report["runner"]["configuration"]["allowUnknownStructuredModel"] = True
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "historical-endpoint.json"
            path.write_text(json.dumps(report), encoding="utf-8")
            with mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "launch", return_value=1) as launch, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(baseline.main(["retry", str(path)]), 1)
            config = launch.call_args[0][0]
            self.assertEqual(config["endpoint"], "https://gateway.example")
            self.assertTrue(config["allowUnknownStructuredModel"])

    def test_retry_revalidates_current_capacity_and_requires_tty_before_build_or_key(self):
        historical = complete_report()
        historical["configuration"]["contextTokens"] = historical["runner"]["configuration"]["contextTokens"] = 8192
        historical["runner"]["profile"]["contextPolicy"]["maxInputTokens"] = 6144
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "historical-budget.json"
            original = json.dumps(historical).encode("utf-8")
            path.write_bytes(original)
            with contextlib.redirect_stderr(io.StringIO()) as error, \
                    mock.patch.object(sys.stdin, "isatty", return_value=True), \
                    mock.patch.object(sys.stderr, "isatty", return_value=True), \
                    mock.patch.object(baseline, "launch") as launch, \
                    mock.patch.object(baseline, "build_runner") as builder, \
                    mock.patch.object(baseline, "read_api_key") as key_reader:
                self.assertEqual(baseline.main(["retry", str(path)]), 2)
                self.assertIn("insufficient_context_capacity", error.getvalue())
                with mock.patch.object(sys.stdin, "isatty", return_value=False):
                    self.assertEqual(baseline.main(["retry", str(path)]), 2)
                launch.assert_not_called()
                builder.assert_not_called()
                key_reader.assert_not_called()
            self.assertEqual(path.read_bytes(), original)


class WireDiagnosticsTests(unittest.TestCase):
    def test_wire_facts_distinguish_upstream_projection_request_and_missing_evidence(self):
        schema_check = schema_check_fixture("invalid")
        upstream = wire_check_fixture()
        projection = wire_check_fixture(json_valid=True, schema_valid=True, matches=False)
        wrong_request = wire_check_fixture()
        wrong_request["request"]["format"]["strictEnabled"] = False
        incomplete = wire_check_fixture()
        incomplete["response"] = {"capture": "incomplete"}
        missing_request = wire_check_fixture()
        missing_request["request"] = {"capture": "unavailable"}
        multiple_legacy = wire_check_fixture(phase="absent", matches=False)
        final = wire_check_fixture(phase="null", json_valid=True, schema_valid=True, matches=False)["response"]["messages"][0]
        final["index"] = 1
        multiple_legacy["response"].update(outputItemCount=2, messageCount=2)
        multiple_legacy["response"]["messages"].append(final)
        for check, expected in (
            (upstream, "该次上游响应未满足已发送的原生 Schema 契约"),
            (projection, "适配器却交出不同文本且校验失败"),
            (wrong_request, "实际出站请求未满足固定原生 Schema 契约"),
            (incomplete, "不能据此区分上游与适配器"),
            (missing_request, "缺少完整出站证据"),
            (multiple_legacy, "终答来源有歧义"),
        ):
            with self.subTest(expected=expected):
                baseline.validate_wire_check(check)
                rendered = "\n".join(baseline.wire_diagnostics(check, schema_check))
                self.assertIn(expected, rendered)
        rendered = "\n".join(baseline.wire_diagnostics(upstream, schema_check))
        self.assertIn("缺失不能证明原生 Schema 是否生效", rendered)
        self.assertIn("不能区分网关改写、参数未透传或所路由模型", rendered)

    def test_wire_selection_uses_final_and_whole_message_not_individual_parts(self):
        schema_check = schema_check_fixture("invalid")
        invalid_final = wire_check_fixture()
        commentary = wire_check_fixture(phase="commentary", json_valid=True, schema_valid=True, matches=False)["response"]["messages"][0]
        invalid_final["response"]["messages"][0]["index"] = 1
        invalid_final["response"]["messages"].insert(0, commentary)
        invalid_final["response"].update(outputItemCount=2, messageCount=2)
        baseline.validate_wire_check(invalid_final)
        rendered = "\n".join(baseline.wire_diagnostics(invalid_final, schema_check))
        self.assertIn("原始答案文本已经不是有效 JSON", rendered)
        self.assertNotIn("原始明确终答满足 Schema", rendered)

        split = wire_check_fixture(json_valid=True, schema_valid=True, matches=False)
        split["response"]["messages"][0].update(textPartCount=2, jsonValidPartCount=0, schemaValidPartCount=0)
        baseline.validate_wire_check(split)
        rendered = "\n".join(baseline.wire_diagnostics(split, schema_check))
        self.assertIn("判定使用整条消息合并后的文本", rendered)
        self.assertIn("原始明确终答满足 Schema", rendered)
        self.assertNotIn("该次上游响应未满足", rendered)

    def test_wire_format_echo_and_text_markers_are_observations_not_repairs(self):
        check = wire_check_fixture()
        check["response"]["format"]["type"] = "text"
        check["response"]["messages"][0].update(leadingBOM=True, codeFence=True)
        baseline.validate_wire_check(check)
        rendered = "\n".join(baseline.wire_diagnostics(check, schema_check_fixture("invalid")))
        self.assertIn("响应回显的格式与出站 json_schema 模式不一致", rendered)
        self.assertIn("前导 UTF-8 BOM", rendered)
        self.assertIn("起始 Markdown 代码围栏", rendered)
        self.assertIn("不是有效 JSON", rendered)

    def test_invalid_wire_fields_types_counts_and_applicability_are_rejected(self):
        mutations = [
            lambda check: check["request"].update(capture="raw-response"),
            lambda check: check["request"].update(capture="unavailable"),
            lambda check: check["request"]["format"].update(type="untrusted-format"),
            lambda check: check["request"]["format"].update(strictEnabled=1),
            lambda check: check["response"].update(status="untrusted-status"),
            lambda check: check["response"].update(messageCount=2),
            lambda check: check["response"].update(messagesTruncated=True),
            lambda check: check["response"]["messages"][0].update(index=1),
            lambda check: check["response"]["messages"][0].update(phase="untrusted-phase"),
            lambda check: check["response"]["messages"][0].update(textPartCount=True),
            lambda check: check["response"]["messages"][0].update(schemaValidPartCount=2),
            lambda check: check["response"]["messages"][0].update(schemaValid=True),
            lambda check: check["response"]["messages"][0].update(raw="private-text"),
        ]
        for mutate in mutations:
            check = wire_check_fixture()
            mutate(check)
            with self.subTest(mutate=mutate), self.assertRaisesRegex(
                    baseline.ProtocolFailure, "^invalid_preflight_wire_check$"):
                baseline.validate_wire_check(check)
        for change in ("provider", "status", "failureCode", "schemaCheck"):
            report = baseline.new_report(configuration(), {})
            baseline.accept_event(report, start_event())
            event = dict(events(preflight_passed=False)[1], failureCode="strict_schema_probe_failed",
                         schemaCheck=schema_check_fixture("invalid"), wireCheck=wire_check_fixture())
            if change == "provider":
                report["configuration"]["provider"] = "anthropic"
            elif change == "status":
                event["status"] = "passed"
                event.pop("schemaCheck")
            elif change == "failureCode":
                event["failureCode"] = "provider_failed"
                event.pop("schemaCheck")
            else:
                event.pop("schemaCheck")
            with self.subTest(change=change), self.assertRaisesRegex(
                    baseline.ProtocolFailure, "^invalid_preflight_wire_check$"):
                baseline.accept_event(report, event)
            self.assertIsNone(report["preflight"])

    def test_bounded_message_list_retains_truncation_and_never_claims_unique_final(self):
        check = wire_check_fixture()
        template = check["response"]["messages"][0]
        check["response"].update(
            outputItemCount=17, messageCount=17, messagesTruncated=True,
            messages=[dict(template, index=index) for index in range(16)],
        )
        baseline.validate_wire_check(check)
        rendered = "\n".join(baseline.wire_diagnostics(check, schema_check_fixture("invalid")))
        self.assertIn("消息摘要达到记录上限", rendered)
        self.assertNotIn("该次上游响应未满足", rendered)


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

    def test_no_http_failure_roundtrips_with_network_advice_in_console_and_markdown(self):
        real_run_child = baseline.run_child

        for code in ("model.upstream_timeout", "provider_failed"):
            records = events(preflight_passed=False)
            records[1].update(failureCode=code, durationMs=30001)

            def fixture_runner(_command, root, config, secret, report, writer):
                program = child_program(records, exit_code=1)
                return real_run_child([sys.executable, "-u", "-c", program], root, config, secret, report, writer)

            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory, \
                    mock.patch.object(baseline, "source_metadata", return_value={}), \
                    mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(baseline.launch(
                    configuration(), Path(directory), get_key=lambda: self.secret, builder=lambda *_: None,
                ), 1)
                report_path = next((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
                original = report_path.read_bytes()
                loaded = baseline.read_report(report_path)
                self.assertEqual(loaded["status"], "failed")
                self.assertEqual(loaded["preflight"]["failureCode"], code)
                self.assertEqual(loaded["preflight"]["durationMs"], 30001)
                self.assertEqual(loaded["samples"], [])
                self.assertEqual(loaded["summary"]["attempted"], 0)
                for absent in ("httpStatus", "responseKind", "schemaCheck", "wireCheck"):
                    self.assertNotIn(absent, loaded["preflight"])
                for rendered in (output.getvalue(), report_path.with_suffix(".md").read_text(encoding="utf-8"),
                                 baseline.render_markdown(loaded)):
                    self.assertIn("阶段 preflight；失败分类 " + code, rendered)
                    self.assertIn("已尝试 0，跳过 2", rendered)
                    self.assertIn("任务尚未开始", rendered)
                    self.assertIn("下一步", rendered)
                    self.assertIn("未记录本次请求的 HTTP 响应状态", rendered)
                    self.assertIn("先检查 endpoint 连通性、本机网络、代理设置和出口", rendered)
                    self.assertNotIn("输出预算 1024", rendered)
                    self.assertNotIn("每任务 1 次", rendered)
                    self.assertNotIn(self.secret, rendered)
                    self.assertNotIn("HTTP 401", rendered)
                    if code == "model.upstream_timeout":
                        self.assertIn("上游请求超时", rendered)
                        self.assertIn("现有证据不足以确定具体阶段", rendered)
                        self.assertIn("不能判断是 DNS 解析、TCP 连接、TLS 协商还是等待 HTTP 响应超时", rendered)
                    else:
                        self.assertIn("现有证据不足以确定具体原因或阶段", rendered)
                        self.assertIn("不能据此判定为超时", rendered)
                        self.assertNotIn("上游请求超时", rendered)
                self.assertEqual(report_path.read_bytes(), original)
                self.assertIn("预检：未通过", output.getvalue())

    def test_upstream_timeout_advice_uses_only_current_stage_http_facts(self):
        for stage, preflight, expected in (
            ("preflight", {"httpStatus": 200, "responseKind": "json"}, "已观察到 HTTP 响应状态"),
            ("preflight", {"httpStatus": 400, "responseKind": "json"}, "已观察到 HTTP 响应状态"),
            ("resume", {"httpStatus": 200, "responseKind": "json"}, "未记录本次请求的 HTTP 响应状态"),
        ):
            with self.subTest(stage=stage, preflight=preflight):
                advice = baseline.failure_advice(stage, "model.upstream_timeout", preflight)
                self.assertIn(expected, advice)
                self.assertIn("代理", advice)
                self.assertIn("出口", advice)
                self.assertNotIn("输出预算", advice)
                self.assertNotIn("每任务", advice)
        for code in ("timeout", "runner_timeout"):
            with self.subTest(code=code):
                advice = baseline.failure_advice("runner", code, {})
                self.assertIn("结合失败阶段", advice)
                self.assertNotIn("输出预算", advice)
                self.assertNotIn("每任务", advice)

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


    def test_wire_check_survives_report_roundtrip_and_explains_the_same_failed_probe(self):
        records = events(preflight_passed=False)
        records[1].update(failureCode="strict_schema_probe_failed", httpStatus=200, responseKind="json",
                          schemaCheck=schema_check_fixture("invalid"), wireCheck=wire_check_fixture())
        real_run_child = baseline.run_child

        def fixture_runner(_command, root, config, secret, report, writer):
            return real_run_child([sys.executable, "-u", "-c", child_program(records, exit_code=1)],
                                  root, config, secret, report, writer)

        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(baseline, "source_metadata", return_value={}), \
                mock.patch.object(baseline, "run_child", side_effect=fixture_runner), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(baseline.launch(configuration(), Path(directory),
                                             get_key=lambda: self.secret, builder=lambda *_: None), 1)
            path = next((Path(directory) / "artifacts" / "agent-baselines").glob("*.json"))
            report = baseline.read_report(path)
            self.assertEqual(report["preflight"]["wireCheck"], records[1]["wireCheck"])
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["summary"]["attempted"], 0)
            comparison, comparable = baseline.comparison_markdown(report, report)
            self.assertFalse(comparable)
            for rendered in (output.getvalue(), path.with_suffix(".md").read_text(encoding="utf-8"), comparison):
                self.assertIn("实际出站契约：text.format.type=json_schema", rendered)
                self.assertIn("该次上游响应未满足已发送的原生 Schema 契约", rendered)
                self.assertNotIn(self.secret, rendered)

    def test_unknown_wire_key_or_value_never_enters_saved_output(self):
        for location in ("key", "value"):
            records = events(preflight_passed=False)
            check = wire_check_fixture()
            if location == "key":
                check[self.secret] = self.secret
            else:
                check["response"]["messages"][0]["phase"] = self.secret
            records[1].update(failureCode="strict_schema_probe_failed",
                              schemaCheck=schema_check_fixture("invalid"), wireCheck=check)
            with self.subTest(location=location), tempfile.TemporaryDirectory() as directory:
                result, writer, _, output = self.invoke(directory, records, exit_code=1)
                self.assertEqual(result["launcherFailure"]["failureCode"], "invalid_preflight_wire_check")
                self.assertIsNone(result["preflight"])
                self.assertNotIn(self.secret, output + writer.json_path.read_text() + writer.md_path.read_text())



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
