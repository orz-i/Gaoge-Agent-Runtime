#!/usr/bin/env python3
"""Interactive, credential-safe launcher for the task-agent live baseline."""

from __future__ import annotations

import datetime as dt
import getpass
import ipaddress
import json
import math
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import unquote, urlsplit
import uuid
import warnings

SCHEMA_VERSION = 1
SERVICE_INPUT_TOKENS = 8192
DEFAULT_ENDPOINTS = {
    "openai": "https://api.openai.com/v1/responses",
    "anthropic": "https://api.anthropic.com/v1/messages",
}
PUBLIC_CONFIGURATION = (
    "provider", "endpoint", "model", "contextTokens", "maxOutputTokens",
    "repetitions", "allowUnknownStructuredModel",
)
MAX_LINE_BYTES = 256 * 1024
MAX_OUTPUT_BYTES = 4 * 1024 * 1024
MAX_EVENTS = 256
PROBE_JSON_TYPES = {"object", "array", "string", "number", "boolean", "null"}
STAGES = {
    "configuration", "preflight", "start", "evidence", "interrupted",
    "lease_check", "resume", "oracle", "terminal_check", "completed",
}
SAMPLE_FIELDS = {
    "sampleID", "caseID", "repetition", "status", "stage", "failureCode",
    "durationMs", "modelCalls", "toolCalls", "oraclePassed", "recoveryChecks",
    "usage", "contextGeneration", "runID", "turnID", "modelDurationsMs",
    "toolExecutions", "lookupCalls", "artifactReads", "draftWrites",
}
RECOVERY_FIELDS = {
    "interruptionObserved", "leaseBlocked", "sameRun", "sameTurn",
    "committedToolsNotReplayed", "noExtraToolsAfterResume", "terminalResumeNoOp",
}


class SafeFailure(Exception):
    """An internal fixed code; never carries a child payload or a credential."""


class ProtocolFailure(SafeFailure):
    pass


class ReportFailure(SafeFailure):
    pass


def repository_root():
    return Path(__file__).resolve().parents[1]


def utc_now():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def safe_environment(root):
    env = dict(os.environ)
    env.pop("OPENAI_API_KEY", None)
    env.pop("ANTHROPIC_API_KEY", None)
    # Go honors PWD when it names the same directory. A logical /tmp PWD and
    # physical /private/tmp GOWORK otherwise disagree on macOS workspace paths.
    env.pop("PWD", None)
    env["GOWORK"] = str(Path(root).resolve() / "go.work")
    return env


def validate_endpoint(provider, value):
    """Validate a full endpoint. Never append paths or resolve DNS."""
    if not isinstance(provider, str) or provider not in DEFAULT_ENDPOINTS:
        raise SafeFailure("invalid_provider")
    if not isinstance(value, str) or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise SafeFailure("invalid_endpoint")
    endpoint = value.strip() or DEFAULT_ENDPOINTS[provider]
    if len(endpoint.encode("utf-8")) > 2048 or any(char.isspace() for char in endpoint) or "?" in endpoint or "#" in endpoint:
        raise SafeFailure("invalid_endpoint")
    try:
        parts = urlsplit(endpoint)
        host = parts.hostname
        port = parts.port
    except ValueError:
        raise SafeFailure("invalid_endpoint") from None
    if not host or parts.scheme not in ("https", "http") or "@" in parts.netloc:
        raise SafeFailure("invalid_endpoint")
    if port is not None and not 1 <= port <= 65535:
        raise SafeFailure("invalid_endpoint")
    loopback = host == "localhost"
    try:
        address = ipaddress.ip_address(host)
        mapped = getattr(address, "ipv4_mapped", None)
        loopback = loopback or address.is_loopback or (mapped is not None and mapped.is_loopback)
    except ValueError:
        pass
    # Match the Go transport's literal localhost check, including its casing.
    if parts.scheme == "http" and (not loopback or (host == "localhost" and parts.netloc.split(":")[0] != "localhost")):
        raise SafeFailure("non_loopback_http")
    path = unquote(parts.path).rstrip("/").lower()
    opposite = "/messages" if provider == "openai" else "/responses"
    if path.endswith("/chat/completions") or path.endswith(opposite):
        raise SafeFailure("endpoint_protocol_mismatch")
    return endpoint


def resolve_interactive_endpoint(provider, value):
    """Expand only standard base addresses entered in the interactive prompt."""
    endpoint = validate_endpoint(provider, value)
    parts = urlsplit(endpoint)
    operation = "responses" if provider == "openai" else "messages"
    if parts.path in ("", "/"):
        path = "/v1/" + operation
    elif parts.path.rstrip("/").endswith("/v1"):
        path = parts.path.rstrip("/") + "/" + operation
    else:
        return endpoint
    return validate_endpoint(provider, parts._replace(path=path).geturl())


def positive_integer(value, minimum=1, maximum=None):
    if type(value) is not int or value < minimum or (maximum is not None and value > maximum):
        raise SafeFailure("invalid_integer")
    return value


def validate_configuration(value, service_input_tokens=SERVICE_INPUT_TOKENS):
    config = {name: value[name] for name in PUBLIC_CONFIGURATION if name in value}
    if len(config) != len(PUBLIC_CONFIGURATION):
        raise SafeFailure("invalid_configuration")
    config["endpoint"] = validate_endpoint(config["provider"], config["endpoint"])
    model = config["model"]
    if not isinstance(model, str) or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}", model) is None:
        raise SafeFailure("invalid_model")
    positive_integer(config["contextTokens"], maximum=1 << 24)
    positive_integer(config["maxOutputTokens"], maximum=1 << 20)
    positive_integer(config["repetitions"], maximum=20)
    available_input = config["contextTokens"] - config["maxOutputTokens"]
    if service_input_tokens is not None:
        positive_integer(service_input_tokens)
    if available_input <= 0 or (service_input_tokens is not None and available_input < service_input_tokens):
        raise SafeFailure("insufficient_context_capacity")
    if type(config["allowUnknownStructuredModel"]) is not bool:
        raise SafeFailure("invalid_probe_choice")
    return config


def prompt_integer(label, default=None, maximum=None):
    while True:
        text = input(label).strip()
        if not text and default is not None:
            return default
        try:
            return positive_integer(int(text), maximum=maximum)
        except (ValueError, SafeFailure):
            print("请输入有效的正整数" + ("（不超过 %d）" % maximum if maximum else "") + "。")


def collect_configuration():
    print("真实模型基线：两个合成任务，验证证据读取、草稿、Context 和中断恢复。")
    while True:
        choice = input("协议 [1=OpenAI Responses, 2=Anthropic Messages，默认 1]: ").strip() or "1"
        if choice in ("1", "2"):
            provider = "openai" if choice == "1" else "anthropic"
            break
        print("请选择 1 或 2。")
    while True:
        entered_endpoint = input("endpoint URL（根域名或 /v1 基地址会补齐；完整自定义路径原样；留空使用官方默认）: ")
        try:
            endpoint = resolve_interactive_endpoint(provider, entered_endpoint)
            print("端点：%s → %s" % (entered_endpoint.strip() or "（官方默认）", endpoint), flush=True)
            break
        except SafeFailure:
            print("端点无效：需要匹配协议的 HTTPS URL；仅显式 loopback 可用 HTTP，不能含账号、query 或 fragment。")
    model = input("精确 model ID: ").strip()
    context_tokens = prompt_integer("该模型已核实的 context capacity（必须填写）: ", maximum=1 << 24)
    print("本次任务输出预算默认 1024，不必填模型最大输出容量。")
    output_tokens = prompt_integer("本次任务每次调用 max output tokens [1024]: ", default=1024, maximum=1 << 20)
    repetitions = prompt_integer("每个任务重复次数 [3，范围 1..20]: ", default=3, maximum=20)
    while True:
        probe = input("允许对 SDK 未登记的模型执行原生严格 JSON Schema 探测？工具能力由后续任务验证。[y/N]: ").strip().lower()
        if probe in ("", "n", "no", "y", "yes"):
            break
        print("请输入 y 或 n。")
    config = validate_configuration({
        "provider": provider, "endpoint": endpoint, "model": model,
        "contextTokens": context_tokens, "maxOutputTokens": output_tokens,
        "repetitions": repetitions, "allowUnknownStructuredModel": probe in ("y", "yes"),
    })
    print("\n配置：")
    for name in PUBLIC_CONFIGURATION:
        print("  %s: %s" % (name, config[name]))
    print("将执行 2 个任务 × %d 次 = %d 个样本，另加 1 次能力探测；会调用真实服务。" %
          (repetitions, 2 * repetitions))
    return config


def read_api_key():
    if not sys.stdin.isatty() or not sys.stderr.isatty():
        raise SafeFailure("interactive_tty_required")
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            key = getpass.getpass("API key（隐藏输入，仅送入子进程 stdin）: ")
    except getpass.GetPassWarning:
        raise SafeFailure("hidden_input_unavailable") from None
    key = key.strip()
    if not key or "\r" in key or "\n" in key or len(key.encode("utf-8")) > 8192:
        raise SafeFailure("invalid_api_key")
    return key


def source_metadata(root):
    env = safe_environment(root)
    try:
        revision = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=root, env=env, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=10,
        ).stdout.strip()
        if not re.fullmatch(r"[0-9a-f]{40,64}", revision):
            revision = None
        dirty = bool(subprocess.run(
            ["git", "status", "--porcelain"], cwd=root, env=env, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=10,
        ).stdout)
        return {"revision": revision, "dirty": dirty}
    except (OSError, subprocess.SubprocessError):
        return {"revision": None, "dirty": None}


def build_runner(root, executable):
    try:
        result = subprocess.run(
            ["go", "build", "-o", str(executable), "./go/agent-runtime-harness/examples/task-agent"],
            cwd=root, env=safe_environment(root), stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=300,
        )
    except (OSError, subprocess.SubprocessError):
        raise SafeFailure("build_failed") from None
    if result.returncode:
        raise SafeFailure("build_failed")


def redact(value, secret):
    if isinstance(value, str):
        return value.replace(secret, "[REDACTED]") if secret else value
    if isinstance(value, list):
        return [redact(item, secret) for item in value]
    if isinstance(value, dict):
        return {redact(key, secret): redact(item, secret) for key, item in value.items()}
    return value


def new_report(config, source):
    return {
        "schemaVersion": SCHEMA_VERSION, "startedAt": utc_now(), "finishedAt": None,
        "source": source, "configuration": dict(config), "runner": {}, "preflight": None,
        "samples": [], "summary": None, "status": "running", "runnerExitCode": None,
        "launcherFailure": None,
    }


def md_cell(value):
    if value is None:
        return "未知"
    if isinstance(value, bool):
        return "是" if value else "否"
    if isinstance(value, (dict, list)):
        value = json.dumps(value, ensure_ascii=False, sort_keys=True, allow_nan=False)
    return str(value).replace("\\", "\\\\").replace("|", "\\|").replace("\r", " ").replace("\n", " ").replace("<", "&lt;").replace(">", "&gt;")


def failure_advice(stage, code, preflight):
    status = preflight.get("httpStatus") if stage == "preflight" else None
    kind = preflight.get("responseKind") if stage == "preflight" else None
    if status is not None:
        if 300 <= status < 400:
            return "端点返回重定向，本工具不跟随；请填写网关文档中的最终 API 地址。"
        if 200 <= status < 300 and kind == "html":
            return "响应声明为 HTML，预期为 API JSON；请核对 API 路径，根域名可能是网页入口。"
        if status in (401, 403):
            return "检查 key 的访问权限，以及网关路由和策略；该状态本身不足以确定密钥是否错误。"
        if status in (400, 422):
            return "按网关文档检查 model ID、输出预算和请求协议；本次任务输出预算可先使用默认 1024。"
        if status == 404:
            return "核对完整 API 地址，以及该网关是否提供所填 model ID。"
        if status == 429:
            return "检查网关配额与调用频率，待限制解除后再运行。"
    if code == "provider_failed":
        return ("网关拒绝、协议不兼容或连接异常均可能导致此分类，现有证据不足以确定具体原因。"
                "请按网关文档核对最终 endpoint、所选协议及 model 的原生严格 JSON Schema 支持；"
                "可先使用输出预算 1024、每任务 1 次重新运行。")
    if code == "strict_schema_probe_failed":
        return ("请确认该 endpoint/model 的原生严格 JSON Schema 实现及最终回答映射；"
                "修正协议适配或改用已验证支持的入口后再采样。"
                "这些事实不能定位网关内部哪一步失效。")
    if code in {"capability_unavailable", "provider_unsupported", "output_schema_invalid"}:
        return "核对模型与网关对原生严格 JSON Schema 的支持；未知模型需明确允许探测，任务样本还会验证工具调用。"
    if code in {"timeout", "runner_timeout"}:
        return "检查网关连接与响应耗时；可先使用输出预算 1024、每任务 1 次重新运行。"
    if code == "build_failed":
        return "先检查本地 Go 工具链与工作区构建配置；编译失败时尚未读取 API key。"
    if code in {"invalid_configuration", "provider_configuration_failed", "context_budget_exceeded"}:
        return "核对 endpoint、model ID、已核实的 context capacity 与本次任务输出预算。"
    if code == "cancelled":
        return "已保留收到的样本；需要完整基线时重新运行，并保留本次部分结果作为记录。"
    if stage == "protocol":
        return "检查启动器与 Go 执行器是否来自同一工作区版本；保留当前报告，重新编译后再运行。"
    if stage in {"oracle", "lease_check", "resume", "terminal_check", "interrupted"}:
        return "查看失败样本的 oraclePassed、recoveryChecks 和工具执行计数，按具体阶段定位任务或恢复检查。"
    return "结合阶段与失败分类检查报告中的配置和样本；修正后重新运行，保留当前结果便于比较。"


def schema_diagnostics(check):
    lines = ["模型回答检查：有内容=%s；有效 JSON=%s；根类型=%s；schema 校验通过=%s。" %
             (md_cell(check["contentPresent"]), md_cell(check["jsonValid"]),
              check["jsonType"], md_cell(check["schemaValid"]))]
    reasons = []
    if not check["contentPresent"]:
        reasons.append("回答内容为空")
    elif not check["jsonValid"]:
        reasons.append("回答不是可解析的单个 JSON 文档")
    elif check["jsonType"] != "object":
        reasons.append("JSON 根类型为 %s，要求 object" % check["jsonType"])
    else:
        if not check["okPresent"]:
            reasons.append("必填字段 ok 缺失")
        elif check["okType"] != "boolean":
            reasons.append("ok 类型为 %s，要求 boolean" % check["okType"])
        elif not check["okIsTrue"]:
            reasons.append("ok 为 false，要求 true")
        if check["extraPropertyCount"]:
            reasons.append("存在 %d 个额外属性，schema 禁止额外属性" % check["extraPropertyCount"])
    if not check["schemaValid"] and not reasons:
        reasons.append("本地 schema 验证器拒绝，当前结构摘要未覆盖该原因")
    if any(check[name] for name in ("toolCallCount", "hostedToolCallCount", "artifactCount")) or check["continue"]:
        reasons.append("纯 JSON 预检要求无工具调用、无产物且 continue=false")
    lines += ["未满足：" + reason + "。" for reason in reasons]
    lines.append("行为检查：工具调用 %d；托管工具调用 %d；产物 %d；continue=%s。" %
                 (check["toolCallCount"], check["hostedToolCallCount"], check["artifactCount"],
                  "true" if check["continue"] else "false"))
    return lines


def failure_diagnostics(report):
    preflight = report.get("preflight") or {}
    failures = []
    if report.get("launcherFailure"):
        failure = report["launcherFailure"]
        failures.append(("启动器", failure["stage"], failure["failureCode"]))
    if preflight.get("status") == "failed":
        failures.append(("原生严格 JSON Schema 预检", "preflight", preflight["failureCode"]))
    error = (report.get("runner") or {}).get("error")
    if error:
        failures.append(("执行器", error["stage"], error["failureCode"]))
    grouped = {}
    for sample in report.get("samples", []):
        if sample["status"] == "failed":
            key = (sample["stage"], sample["failureCode"])
            grouped[key] = grouped.get(key, 0) + 1
    for (stage, code), count in grouped.items():
        failures.append(("%d 个任务样本" % count, stage, code))
    if not failures:
        return []
    summary = report.get("summary") or {}
    if summary:
        lines = ["任务样本：请求 %d，已尝试 %d，跳过 %d。" %
                 (summary["requested"], summary["attempted"], summary["skipped"])]
    else:
        lines = ["已收到 %d 个样本结果；已尝试与跳过总数未知（缺少最终汇总）。" % len(report.get("samples", []))]
    if preflight.get("status") == "failed" and not report.get("samples"):
        lines.append("任务尚未开始：暂无任务完成率证据。")
    if "httpStatus" in preflight:
        lines.append("预检传输事实：HTTP %d；响应 Content-Type 归类 %s。" %
                     (preflight["httpStatus"], preflight["responseKind"]))
    for label, stage, code in failures:
        lines.append("%s：阶段 %s；失败分类 %s。" % (label, stage, code))
        if stage == "preflight" and code == "strict_schema_probe_failed":
            if "schemaCheck" in preflight:
                lines += schema_diagnostics(preflight["schemaCheck"])
            else:
                lines.append("本报告未记录 schema 细项，无法从已存数据还原具体违例。")
        lines.append("下一步：" + failure_advice(stage, code, preflight))
    return lines


def render_markdown(report):
    summary = report.get("summary") or {}
    preflight = report.get("preflight") or {}
    runner = report.get("runner") or {}
    lines = [
        "# 真实模型 Agent 基线", "",
        "- 状态：%s" % md_cell(report.get("status")),
        "- 开始：%s；结束：%s" % (md_cell(report.get("startedAt")), md_cell(report.get("finishedAt"))),
        "- 代码：%s；工作区有修改：%s" % (md_cell(report.get("source", {}).get("revision")), md_cell(report.get("source", {}).get("dirty"))),
        "- 任务版本：%s；数据集：%s" % (md_cell(runner.get("taskVersion")), md_cell(runner.get("datasetHash"))),
        "", "## 配置", "", "| 项目 | 值 |", "| --- | --- |",
    ]
    for key, value in report.get("configuration", {}).items():
        lines.append("| %s | %s |" % (md_cell(key), md_cell(value)))
    lines += [
        "", "## 结果", "",
        "- 能力探测：%s；失败分类：%s" % (md_cell(preflight.get("status")), md_cell(preflight.get("failureCode"))),
        "- 请求 %s，已尝试 %s，通过 %s，失败 %s，未执行 %s。" % tuple(md_cell(summary.get(key)) for key in ("requested", "attempted", "passed", "failed", "skipped")),
        "- 尝试样本成功率：%s（通过 / 已尝试；不把未执行样本视为成功）。" % md_cell(summary.get("successRate")),
        "- 耗时摘要（毫秒）：%s" % md_cell(summary.get("durationMs")),
        "- 模型调用 %s；工具执行 %s。" % (md_cell(summary.get("modelCalls")), md_cell(summary.get("toolCalls"))),
        "- 任务调用 usage 覆盖：%s；能力探测 usage 单列：%s。" % (md_cell(summary.get("usage")), md_cell(preflight.get("usage"))),
    ]
    if report.get("launcherFailure"):
        lines.append("- 启动器失败：%s" % md_cell(report["launcherFailure"]))
    if runner.get("error"):
        lines.append("- 执行器失败：%s" % md_cell(runner["error"]))
    diagnostics = failure_diagnostics(report)
    if diagnostics:
        lines += ["", "## 失败诊断与下一步", ""] + ["- " + md_cell(line) for line in diagnostics]
    lines += [
        "", "## 样本", "",
        "| 样本 | 任务 | 重复 | 状态 | 阶段 | 失败分类 | 毫秒 | 模型调用 | 工具调用 | Oracle |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for sample in report["samples"]:
        lines.append("| " + " | ".join(md_cell(sample.get(key)) for key in (
            "sampleID", "caseID", "repetition", "status", "stage", "failureCode",
            "durationMs", "modelCalls", "toolCalls", "oraclePassed",
        )) + " |")
    lines += [
        "", "## 可比性与证据边界", "",
        "只有相同任务版本、数据集、完整执行 profile 和输出上限的报告适合数值横向比较。"
        "模型、provider、endpoint 与代码版本可以不同，必须保留这些差异。"
        "Context capacity 不同时，服务输入上限仍须一致。",
        "",
        "这两个合成任务使用明确的 oracle；结果不等于通用模型能力评分。"
        "恢复模式是同一进程内重新组装并使用测试 Store 时钟，"
        "不代表真实持久化进程恢复，也不声称外部副作用 exactly-once。",
        "",
        "未完成、取消、协议错误或未知 schema 的报告只能展示已经观察到的样本，"
        "不能据此给出总体提升结论。未知 token usage 保持未知，不按零计费量处理。",
        "",
    ]
    return "\n".join(lines)


def atomic_private_write(path, text):
    temp_path = None
    try:
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor, name = tempfile.mkstemp(prefix="." + path.name + ".", dir=str(path.parent))
        temp_path = Path(name)
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
            os.chmod(temp_path, 0o600)
            stream.write(text)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(str(temp_path), str(path))
        temp_path = None
    except OSError:
        raise ReportFailure("report_write_failed") from None
    finally:
        if temp_path is not None:
            try:
                temp_path.unlink()
            except OSError:
                pass


class ReportWriter:
    def __init__(self, directory):
        name = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
        self.json_path = Path(directory) / (name + ".json")
        self.md_path = Path(directory) / (name + ".md")

    def save(self, report, secret=""):
        safe = redact(report, secret)
        try:
            encoded = json.dumps(safe, ensure_ascii=False, indent=2, allow_nan=False) + "\n"
            markdown = render_markdown(safe)
        except (TypeError, ValueError, RecursionError):
            raise ReportFailure("report_encoding_failed") from None
        atomic_private_write(self.json_path, encoded)
        atomic_private_write(self.md_path, markdown)


def reject_constant(_):
    raise ValueError("nonfinite")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate_key")
        result[key] = value
    return result


def parse_json(text):
    return json.loads(text, parse_constant=reject_constant, object_pairs_hook=unique_object)


def nonnegative_number(value):
    if type(value) is int:
        return 0 <= value <= (1 << 62)
    return type(value) is float and math.isfinite(value) and value >= 0


def valid_code(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_.-]{1,100}", value) is not None


def validate_usage(value, coverage=False):
    token_fields = {"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens"}
    if not coverage:
        if value is None:
            return
        if not isinstance(value, dict) or set(value) != token_fields or any(
                type(number) is not int or not 0 <= number <= (1 << 62) for number in value.values()):
            raise ProtocolFailure("invalid_usage")
        return
    if (not isinstance(value, dict) or
            set(value) != {"knownCalls", "missingCalls", "complete", "known"} or
            any(type(value.get(name)) is not int or value[name] < 0 for name in ("knownCalls", "missingCalls")) or
            type(value.get("complete")) is not bool or value["complete"] != (value["missingCalls"] == 0) or
            (value["knownCalls"] == 0) != (value["known"] is None)):
        raise ProtocolFailure("invalid_usage")
    validate_usage(value["known"])


def validate_schema_check(check):
    required = {
        "contentPresent", "jsonValid", "jsonType", "schemaValid", "toolCallCount",
        "hostedToolCallCount", "artifactCount", "continue",
    }
    if not isinstance(check, dict) or not required <= set(check):
        raise ProtocolFailure("invalid_preflight_schema_check")
    if (any(type(check[name]) is not bool for name in ("contentPresent", "jsonValid", "schemaValid", "continue")) or
            not isinstance(check["jsonType"], str) or check["jsonType"] not in PROBE_JSON_TYPES | {"invalid"} or
            check["jsonValid"] != (check["jsonType"] != "invalid") or
            (check["jsonValid"] and not check["contentPresent"]) or
            (check["schemaValid"] and not check["jsonValid"]) or
            any(type(check[name]) is not int or not 0 <= check[name] <= (1 << 62)
                for name in ("toolCallCount", "hostedToolCallCount", "artifactCount"))):
        raise ProtocolFailure("invalid_preflight_schema_check")
    if check["jsonType"] == "object":
        required |= {"okPresent", "okType", "extraPropertyCount"}
        if (type(check.get("okPresent")) is not bool or
                not isinstance(check.get("okType"), str) or check["okType"] not in PROBE_JSON_TYPES | {"missing"} or
                check["okPresent"] != (check["okType"] != "missing") or
                type(check.get("extraPropertyCount")) is not int or not 0 <= check["extraPropertyCount"] <= (1 << 62)):
            raise ProtocolFailure("invalid_preflight_schema_check")
        if check["okType"] == "boolean":
            required.add("okIsTrue")
            if type(check.get("okIsTrue")) is not bool:
                raise ProtocolFailure("invalid_preflight_schema_check")
    if set(check) != required:
        raise ProtocolFailure("invalid_preflight_schema_check")


def validate_sample_diagnostics(sample):
    validate_usage(sample.get("usage"), coverage=True)
    for name in ("lookupCalls", "artifactReads", "draftWrites", "contextGeneration"):
        if name in sample and (type(sample[name]) is not int or sample[name] < 0):
            raise ProtocolFailure("invalid_sample_diagnostics")
    if "modelDurationsMs" in sample and (
            not isinstance(sample["modelDurationsMs"], list) or
            len(sample["modelDurationsMs"]) > 100 or
            any(type(value) is not int or value < 0 for value in sample["modelDurationsMs"])):
        raise ProtocolFailure("invalid_sample_diagnostics")
    if "toolExecutions" in sample:
        if not isinstance(sample["toolExecutions"], list) or len(sample["toolExecutions"]) > 100:
            raise ProtocolFailure("invalid_sample_diagnostics")
        for execution in sample["toolExecutions"]:
            if (not isinstance(execution, dict) or
                    set(execution) != {"toolKey", "callIDHash", "executions"} or
                    not isinstance(execution["toolKey"], str) or not execution["toolKey"] or
                    not isinstance(execution["callIDHash"], str) or
                    not re.fullmatch(r"sha256:[0-9a-f]{64}", execution["callIDHash"]) or
                    type(execution["executions"]) is not int or execution["executions"] < 1):
                raise ProtocolFailure("invalid_sample_diagnostics")


def accept_event(report, event):
    if not isinstance(event, dict) or not isinstance(event.get("type"), str) or report["summary"] is not None:
        raise ProtocolFailure("invalid_event")
    kind = event["type"]
    runner = report["runner"]
    if kind == "start":
        if runner.get("taskVersion") or type(event.get("schemaVersion")) is not int or event["schemaVersion"] != 1:
            raise ProtocolFailure("invalid_start")
        for name in ("taskVersion", "datasetHash"):
            if not isinstance(event.get(name), str) or not event[name] or len(event[name]) > 256:
                raise ProtocolFailure("invalid_start")
        if not isinstance(event.get("profile"), dict) or not event["profile"] or not isinstance(event.get("configuration"), dict):
            raise ProtocolFailure("invalid_start")
        if type(event.get("requestedSamples")) is not int or event["requestedSamples"] != 2 * report["configuration"]["repetitions"]:
            raise ProtocolFailure("sample_count_mismatch")
        configuration = event["configuration"]
        for name in ("provider", "model", "contextTokens", "maxOutputTokens", "repetitions", "allowUnknownStructuredModel"):
            if configuration.get(name) != report["configuration"][name]:
                raise ProtocolFailure("configuration_mismatch")
        public = {name: configuration[name] for name in (
            "provider", "model", "endpointOrigin", "endpointHash", "contextTokens",
            "maxOutputTokens", "repetitions", "allowUnknownStructuredModel",
        ) if name in configuration}
        report["runner"] = {name: event[name] for name in ("schemaVersion", "taskVersion", "datasetHash", "profile", "requestedSamples")}
        report["runner"]["configuration"] = public
    elif kind == "error":
        if not valid_code(event.get("stage")) or not valid_code(event.get("failureCode")):
            raise ProtocolFailure("invalid_error_event")
        if runner.get("error"):
            raise ProtocolFailure("duplicate_error_event")
        runner["error"] = {name: event[name] for name in ("stage", "failureCode")}
    elif kind == "preflight":
        if not runner.get("taskVersion") or report["preflight"] is not None or report["samples"]:
            raise ProtocolFailure("invalid_preflight_order")
        if event.get("status") not in ("passed", "failed") or not nonnegative_number(event.get("durationMs")):
            raise ProtocolFailure("invalid_preflight")
        if event["status"] == "failed" and not valid_code(event.get("failureCode")):
            raise ProtocolFailure("invalid_preflight")
        validate_usage(event.get("usage"))
        if ("httpStatus" in event) != ("responseKind" in event):
            raise ProtocolFailure("invalid_preflight_transport")
        if "httpStatus" in event and (
                type(event["httpStatus"]) is not int or not 100 <= event["httpStatus"] <= 999 or
                event["responseKind"] not in ("json", "html", "event_stream", "other")):
            raise ProtocolFailure("invalid_preflight_transport")
        if "schemaCheck" in event:
            if event["status"] != "failed" or event.get("failureCode") != "strict_schema_probe_failed":
                raise ProtocolFailure("invalid_preflight_schema_check")
            validate_schema_check(event["schemaCheck"])
        report["preflight"] = {name: event[name] for name in (
            "status", "failureCode", "durationMs", "usage", "httpStatus", "responseKind", "schemaCheck",
        ) if name in event}
    elif kind == "sample":
        if not runner.get("taskVersion") or not report["preflight"] or report["preflight"]["status"] != "passed":
            raise ProtocolFailure("invalid_sample_order")
        sample = event.get("sample")
        if not isinstance(sample, dict) or sample.get("status") not in ("passed", "failed") or not isinstance(sample.get("stage"), str) or sample["stage"] not in STAGES:
            raise ProtocolFailure("invalid_sample")
        if any(not isinstance(sample.get(name), str) or not sample[name] or len(sample[name]) > 256 for name in ("sampleID", "caseID")):
            raise ProtocolFailure("invalid_sample")
        if type(sample.get("repetition")) is not int or not 1 <= sample["repetition"] <= report["configuration"]["repetitions"]:
            raise ProtocolFailure("invalid_sample")
        if type(sample.get("oraclePassed")) is not bool or (sample["status"] == "passed" and not sample["oraclePassed"]):
            raise ProtocolFailure("invalid_sample")
        if not nonnegative_number(sample.get("durationMs")) or any(type(sample.get(name)) is not int or sample[name] < 0 for name in ("modelCalls", "toolCalls")):
            raise ProtocolFailure("invalid_sample")
        if sample["status"] == "failed" and not valid_code(sample.get("failureCode")):
            raise ProtocolFailure("invalid_sample")
        checks = sample.get("recoveryChecks")
        if not isinstance(checks, dict) or set(checks) != RECOVERY_FIELDS or any(type(value) is not bool for value in checks.values()):
            raise ProtocolFailure("invalid_sample")
        validate_sample_diagnostics(sample)
        if len(report["samples"]) >= runner["requestedSamples"] or any(
                item["sampleID"] == sample["sampleID"] or
                (item["caseID"], item["repetition"]) == (sample["caseID"], sample["repetition"])
                for item in report["samples"]):
            raise ProtocolFailure("duplicate_or_extra_sample")
        if len({item["caseID"] for item in report["samples"]} | {sample["caseID"]}) > 2:
            raise ProtocolFailure("unexpected_case")
        report["samples"].append({name: value for name, value in sample.items() if name in SAMPLE_FIELDS})
    elif kind == "summary":
        summary = event.get("summary")
        if not runner.get("taskVersion") or report["preflight"] is None or not isinstance(summary, dict):
            raise ProtocolFailure("invalid_summary")
        for name in ("requested", "attempted", "passed", "failed", "skipped"):
            if type(summary.get(name)) is not int or summary[name] < 0:
                raise ProtocolFailure("invalid_summary")
        if (summary["requested"] != runner["requestedSamples"] or
                summary["attempted"] != len(report["samples"]) or
                summary["passed"] != sum(item["status"] == "passed" for item in report["samples"]) or
                summary["failed"] != sum(item["status"] == "failed" for item in report["samples"]) or
                summary["attempted"] + summary["skipped"] != summary["requested"] or
                type(summary.get("complete")) is not bool):
            raise ProtocolFailure("summary_count_mismatch")
        if summary["complete"] and (summary["attempted"] != summary["requested"] or summary["skipped"] != 0):
            raise ProtocolFailure("partial_marked_complete")
        durations = summary.get("durationMs")
        if not isinstance(durations, dict) or any(type(durations.get(name)) is not int or durations[name] < 0 for name in ("count", "total", "min", "max", "p50", "p95")):
            raise ProtocolFailure("invalid_duration_summary")
        if durations["count"] != summary["attempted"]:
            raise ProtocolFailure("invalid_duration_summary")
        expected_rate = summary["passed"] / summary["attempted"] if summary["attempted"] else None
        rate = summary.get("successRate")
        if (expected_rate is None and rate is not None) or (expected_rate is not None and (not nonnegative_number(rate) or abs(rate - expected_rate) > 1e-9)):
            raise ProtocolFailure("summary_rate_mismatch")
        validate_usage(summary.get("usage"), coverage=True)
        for name in ("modelCalls", "toolCalls"):
            if name in summary and (type(summary[name]) is not int or summary[name] != sum(item[name] for item in report["samples"])):
                raise ProtocolFailure("summary_call_count_mismatch")
        report["summary"] = {name: summary[name] for name in (
            "requested", "attempted", "passed", "failed", "skipped", "complete",
            "successRate", "durationMs", "usage", "modelCalls", "toolCalls",
        ) if name in summary}
    else:
        raise ProtocolFailure("unknown_event")


def finish_report(report, exit_code, failure=None):
    report["finishedAt"] = utc_now()
    report["runnerExitCode"] = exit_code
    report["launcherFailure"] = failure
    summary = report["summary"]
    if failure:
        code = failure["failureCode"]
        report["status"] = ("interrupted" if code == "cancelled" else
                            "failed" if code in {"build_failed", "interactive_tty_required", "hidden_input_unavailable", "invalid_api_key", "launcher_io_failed"} else "incomplete")
    elif summary is not None:
        all_passed = summary["complete"] and summary["requested"] == summary["passed"] and summary["skipped"] == 0
        if (exit_code == 0) != all_passed:
            report["status"] = "incomplete"
            report["launcherFailure"] = {"stage": "protocol", "failureCode": "exit_summary_mismatch"}
        else:
            known_failure = summary["complete"] or (report.get("preflight") or {}).get("status") == "failed"
            report["status"] = "passed" if all_passed else ("failed" if known_failure else "incomplete")
    elif report["runner"].get("error") and not report["samples"] and exit_code in (1, 2):
        report["status"] = "failed"
    else:
        report["status"] = "incomplete"
        report["launcherFailure"] = {"stage": "protocol", "failureCode": "missing_summary"}


def read_output(stream, events, stop):
    total = 0

    def send(kind, value):
        while not stop.is_set():
            try:
                events.put((kind, value), timeout=0.1)
                return True
            except queue.Full:
                continue
        return False

    try:
        while not stop.is_set():
            line = stream.readline(MAX_LINE_BYTES + 1)
            if not line:
                send("eof", None)
                return
            total += len(line)
            if len(line) > MAX_LINE_BYTES or total > MAX_OUTPUT_BYTES:
                send("failure", "output_limit")
                return
            if not send("line", line):
                return
    except OSError:
        send("failure", "output_read_failed")


def terminate_and_reap(process):
    if process is None:
        return
    try:
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        try:
            process.kill()
            process.wait(timeout=5)
        except (OSError, subprocess.TimeoutExpired):
            pass


def decode_event(line):
    try:
        return parse_json(line.decode("utf-8"))
    except (UnicodeError, ValueError, RecursionError):
        raise ProtocolFailure("invalid_jsonl") from None


def retain_queued_samples(events, reader, report, writer, secret, remaining):
    """Keep already emitted records when cancellation stops the main reader."""
    deadline = time.monotonic() + 1
    while remaining > 0 and time.monotonic() < deadline:
        try:
            kind, value = events.get(timeout=0.1)
        except queue.Empty:
            if reader is None or not reader.is_alive():
                return
            continue
        if kind != "line":
            return
        remaining -= 1
        try:
            accept_event(report, decode_event(value))
        except ProtocolFailure:
            return
        writer.save(report, secret)


def run_child(command, root, config, secret, report, writer, timeout_seconds=None):
    process = None
    reader = None
    stop = threading.Event()
    failure = None
    events = queue.Queue(maxsize=16)
    seen = 0
    deadline = time.monotonic() + (timeout_seconds if timeout_seconds is not None else 90 + 600 * config["repetitions"])
    try:
        writer.save(report, secret)
        process = subprocess.Popen(
            command, cwd=root, env=safe_environment(root), stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=(os.name != "nt"),
        )
        reader = threading.Thread(target=read_output, args=(process.stdout, events, stop), daemon=True)
        reader.start()
        payload = dict(config, schemaVersion=1, apiKey=secret)
        encoded = json.dumps(payload, ensure_ascii=False, allow_nan=False).encode("utf-8") + b"\n"
        process.stdin.write(encoded)
        process.stdin.close()
        del payload, encoded
        while True:
            if time.monotonic() >= deadline:
                raise SafeFailure("runner_timeout")
            try:
                kind, value = events.get(timeout=0.1)
            except queue.Empty:
                continue
            if kind == "eof":
                break
            if kind == "failure":
                raise ProtocolFailure(value)
            seen += 1
            if seen > MAX_EVENTS:
                raise ProtocolFailure("event_limit")
            event = decode_event(value)
            accept_event(report, event)
            writer.save(report, secret)
            if event["type"] == "preflight":
                preflight = redact(report["preflight"], secret)
                detail = "；失败分类 " + preflight["failureCode"] if preflight["status"] == "failed" else ""
                print("预检：%s；阶段 preflight%s。" %
                      ("通过" if preflight["status"] == "passed" else "未通过", detail), flush=True)
            elif event["type"] == "sample":
                sample = redact(report["samples"][-1], secret)
                detail = "；失败分类 " + sample["failureCode"] if sample["status"] == "failed" else ""
                print("样本 %d：%s，第 %s 次，%s，阶段 %s%s。" %
                      (len(report["samples"]), sample["caseID"], sample["repetition"], sample["status"], sample["stage"], detail), flush=True)
        process.wait(timeout=max(0.1, min(5, deadline - time.monotonic())))
    except KeyboardInterrupt:
        failure = {"stage": "launcher", "failureCode": "cancelled"}
    except ProtocolFailure as error:
        failure = {"stage": "protocol", "failureCode": str(error)}
    except SafeFailure as error:
        if isinstance(error, ReportFailure):
            raise
        failure = {"stage": "launcher", "failureCode": str(error)}
    except (OSError, subprocess.SubprocessError):
        failure = {"stage": "launcher", "failureCode": "runner_process_failed"}
    finally:
        terminate_and_reap(process)
        try:
            if failure and failure["failureCode"] in ("cancelled", "runner_timeout", "runner_process_failed"):
                retain_queued_samples(events, reader, report, writer, secret, MAX_EVENTS - seen)
        finally:
            stop.set()
            if reader is not None:
                reader.join(timeout=1)
            if process is not None:
                for stream in (process.stdin, process.stdout):
                    if stream is not None:
                        try:
                            stream.close()
                        except OSError:
                            pass
    finish_report(report, process.returncode if process is not None else None, failure)
    safe = redact(report, secret)
    report.clear()
    report.update(safe)
    writer.save(report, secret)
    return report


def launch(config, root, get_key=None, builder=None):
    config = validate_configuration(config)
    get_key = get_key or read_api_key
    builder = builder or build_runner
    writer = ReportWriter(Path(root) / "artifacts" / "agent-baselines")
    report = new_report(config, source_metadata(root))
    secret = ""
    try:
        with tempfile.TemporaryDirectory(prefix="gaoge-agent-baseline-") as directory:
            executable = Path(directory) / ("task-agent.exe" if os.name == "nt" else "task-agent")
            print("正在编译当前工作区；编译成功后才读取 API key。", flush=True)
            builder(root, executable)
            secret = get_key()
            run_child([str(executable), "-mode=baseline-stdin"], root, config, secret, report, writer)
    except KeyboardInterrupt:
        finish_report(report, None, {"stage": "launcher", "failureCode": "cancelled"})
        writer.save(report, secret)
    except ReportFailure:
        print("报告无法保存；执行已停止。请检查 artifacts/agent-baselines 的可写性。", file=sys.stderr)
        return 1
    except SafeFailure as error:
        finish_report(report, None, {"stage": "launcher", "failureCode": str(error)})
        report["status"] = "failed"
        writer.save(report, secret)
    except OSError:
        finish_report(report, None, {"stage": "launcher", "failureCode": "launcher_io_failed"})
        try:
            writer.save(report, secret)
        except ReportFailure:
            print("报告无法保存；执行已停止。", file=sys.stderr)
            return 1
    safe = redact(report, secret)
    print("状态：%s" % safe["status"])
    for line in failure_diagnostics(safe):
        print(line)
    print("JSON 报告：%s\nMarkdown 报告：%s" % (writer.json_path, writer.md_path))
    return 0 if report["status"] == "passed" else 1


def validate_report(report):
    if not isinstance(report, dict) or type(report.get("schemaVersion")) is not int or report["schemaVersion"] != 1:
        raise SafeFailure("unknown_report_schema")
    if (not isinstance(report.get("source"), dict) or
            not isinstance(report.get("configuration"), dict) or
            not isinstance(report.get("runner"), dict) or
            not isinstance(report.get("samples"), list) or
            (report.get("summary") is not None and not isinstance(report["summary"], dict)) or
            (report.get("preflight") is not None and not isinstance(report["preflight"], dict))):
        raise SafeFailure("invalid_report")
    try:
        runner = report["runner"]
        # Historical runs keep their own execution budget. A checkpoint before
        # start has no measured profile and cannot be numerically compared.
        service_input_tokens = None
        if runner.get("taskVersion"):
            service_input_tokens = runner["profile"]["contextPolicy"]["maxInputTokens"]
            positive_integer(service_input_tokens)
        config = validate_configuration(report["configuration"], service_input_tokens=service_input_tokens)
        rebuilt = new_report(config, report["source"])
        if runner.get("taskVersion"):
            accept_event(rebuilt, dict(runner, type="start"))
        if report["preflight"] is not None:
            accept_event(rebuilt, dict(report["preflight"], type="preflight"))
        for sample in report["samples"]:
            accept_event(rebuilt, {"type": "sample", "sample": sample})
        if runner.get("error") is not None:
            if not isinstance(runner["error"], dict):
                raise SafeFailure("invalid_report")
            accept_event(rebuilt, dict(runner["error"], type="error"))
        if report["summary"] is not None:
            accept_event(rebuilt, {"type": "summary", "summary": report["summary"]})
        failure = report.get("launcherFailure")
        if failure is not None and (
                not isinstance(failure, dict) or not valid_code(failure.get("stage")) or
                not valid_code(failure.get("failureCode"))):
            raise SafeFailure("invalid_report")
        exit_code = report.get("runnerExitCode")
        if exit_code is not None and type(exit_code) is not int:
            raise SafeFailure("invalid_report")
        if report.get("status") == "running":
            # A checkpoint may be the last file left after an external kill.
            if report.get("finishedAt") is not None:
                raise SafeFailure("invalid_report")
        else:
            finish_report(rebuilt, exit_code, failure)
            if rebuilt["status"] != report.get("status"):
                raise SafeFailure("report_status_mismatch")
    except (KeyError, TypeError, AttributeError, ValueError, RecursionError):
        raise SafeFailure("invalid_report") from None
    return report


def read_report(path):
    try:
        if Path(path).stat().st_size > MAX_OUTPUT_BYTES:
            raise SafeFailure("report_too_large")
        report = parse_json(Path(path).read_text(encoding="utf-8"))
    except (OSError, UnicodeError, ValueError, RecursionError):
        raise SafeFailure("invalid_report") from None
    return validate_report(report)


def comparison_markdown(old, new):
    validate_report(old)
    validate_report(new)
    lines = ["# 模型基线比较", "", "| 配置 | 基线 | 新报告 |", "| --- | --- | --- |"]
    for name in ("provider", "endpoint", "model", "contextTokens", "maxOutputTokens", "repetitions"):
        lines.append("| %s | %s | %s |" % (name, md_cell(old["configuration"].get(name)), md_cell(new["configuration"].get(name))))
    lines.append("| revision | %s | %s |" % (md_cell(old.get("source", {}).get("revision")), md_cell(new.get("source", {}).get("revision"))))
    incompatible = []
    for name in ("taskVersion", "datasetHash", "profile"):
        left, right = old["runner"].get(name), new["runner"].get(name)
        if left is None or right is None or left != right:
            incompatible.append(name)
    if old["configuration"].get("maxOutputTokens") != new["configuration"].get("maxOutputTokens"):
        incompatible.append("maxOutputTokens")
    if incompatible:
        lines += ["", "无法进行数值比较：%s 不一致或缺失。请使用相同任务、数据集、执行策略和输出上限重新采样。" % "、".join(incompatible), ""]
        return "\n".join(lines), False
    if old["configuration"].get("contextTokens") != new["configuration"].get("contextTokens"):
        lines += ["", "两边 context capacity 不同；执行 profile（含固定服务输入上限）相同，请将容量差异保留为解释条件。"]
    complete = all(
        report.get("status") in ("passed", "failed") and
        isinstance(report.get("summary"), dict) and report["summary"].get("complete") is True
        for report in (old, new)
    )
    lines += ["", "## 已观察结果", "", "| 项目 | 基线 | 新报告 |", "| --- | --- | --- |"]
    for name in ("status",):
        lines.append("| %s | %s | %s |" % (name, md_cell(old.get(name)), md_cell(new.get(name))))
    for name in ("requested", "attempted", "passed", "failed", "skipped", "successRate", "durationMs", "modelCalls", "toolCalls", "usage"):
        lines.append("| %s | %s | %s |" % (name, md_cell((old.get("summary") or {}).get(name)), md_cell((new.get("summary") or {}).get(name))))
    if not complete:
        lines += ["", "至少一份报告未完整完成。本表只列已观察到的样本，不能给出总体提升、排名或完整基线结论。"]
    else:
        lines += ["", "成功率的分母为各自已尝试样本；同时列出请求和未执行数。耗时为同一合成任务上的观察值，没有统计显著性或自动胜者结论。"]
    for label, report in (("基线", old), ("新报告", new)):
        diagnostics = failure_diagnostics(report)
        if diagnostics:
            lines += ["", "### " + label + "失败诊断", ""] + ["- " + md_cell(line) for line in diagnostics]
    lines += ["", "这不是通用能力排名。能力探测费用与任务 usage 分开；未知 usage 不按零处理。", ""]
    return "\n".join(lines), complete


def main(argv=None):
    args = list(sys.argv[1:] if argv is None else argv)
    try:
        if args in (["--help"], ["-h"]):
            print("用法：python3 scripts/run-agent-baseline.py [compare old.json new.json]\n"
                  "无参数交互运行；compare 离线读取报告，不编译、不读取 key、不会联网。")
            return 0
        if args:
            if len(args) != 3 or args[0] != "compare":
                print("用法：python3 scripts/run-agent-baseline.py [compare old.json new.json]", file=sys.stderr)
                return 2
            rendered, complete = comparison_markdown(read_report(args[1]), read_report(args[2]))
            print(rendered)
            return 0 if complete else 2
        if not sys.stdin.isatty() or not sys.stderr.isatty():
            raise SafeFailure("interactive_tty_required")
        return launch(collect_configuration(), repository_root())
    except KeyboardInterrupt:
        print("\n已取消；尚未启动的新采样不会执行。", file=sys.stderr)
        return 1
    except (SafeFailure, ReportFailure) as error:
        print("无法完成操作（%s）。未记录凭据或子进程原始错误。" % str(error), file=sys.stderr)
        return 2
    except (OSError, EOFError):
        print("终端输入或报告文件操作失败；未记录原始异常。", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
