#!/usr/bin/env python3
"""Verify frozen, public Agent-quality evidence anchors without publishing raw reports.

Default: validate the committed manifest only. --evidence-dir additionally reads
operator-owned, untracked reports, checks their original bytes, and passes each
through the existing strict Agent report protocol before checking public facts.
Never sends model requests, modifies reports, or prints response/source content.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / "benchmarks" / "agent-quality-anchors-v1.json"
FIELD_NAMES = {"id", "filename", "sha256", "sourceRevision", "sourceDirty",
               "taskVersion", "datasetHash", "status", "pilotOnly", "pilotCaseID",
               "configuration", "summary"}
CONFIG_FIELDS = {"provider", "endpoint", "model", "contextTokens",
                 "maxOutputTokens", "repetitions", "suite", "reasoningEffort"}
SUMMARY_FIELDS = {"requested", "attempted", "passed", "failed", "skipped",
                  "modelCalls", "toolCalls", "durationP50Ms", "durationP95Ms"}
EXPECTED_IDS = {"2026-11-07-full-40-historical",
                "2026-11-08-distractor05-pilot-success"}


def baseline_module():
    spec = importlib.util.spec_from_file_location("agent_baseline_evidence_parser",
                                                  ROOT / "scripts" / "run-agent-baseline.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def require(condition, code):
    if not condition:
        raise ValueError(code)


def load_anchors(path=MANIFEST):
    obj = baseline_module().parse_json(Path(path).read_text(encoding="utf-8"))
    require(isinstance(obj, dict) and set(obj) == {"schemaVersion", "description", "records"}
            and type(obj["schemaVersion"]) is int and obj["schemaVersion"] == 1
            and type(obj["description"]) is str, "invalid_manifest_envelope")
    records = obj["records"]
    require(isinstance(records, list) and len(records) == 2, "invalid_manifest_count")
    seen = set()
    filenames = set()
    for anchor in records:
        require(isinstance(anchor, dict) and set(anchor) == FIELD_NAMES, "invalid_anchor_fields")
        require(type(anchor["id"]) is str and anchor["id"] in EXPECTED_IDS
                and anchor["id"] not in seen, "invalid_anchor_identity")
        seen.add(anchor["id"])
        filename = anchor["filename"]
        require(type(filename) is str and re.fullmatch(r"20\d{6}T\d{6}Z-[0-9a-f]{8}\.json", filename)
                and filename not in filenames, "invalid_anchor_filename")
        filenames.add(filename)
        require(all(type(anchor[k]) is str and re.fullmatch(r"[a-f0-9]{64}", anchor[k])
                    for k in ("sha256",)), "invalid_anchor_hash")
        require(type(anchor["sourceRevision"]) is str
                and re.fullmatch(r"[a-f0-9]{40}", anchor["sourceRevision"])
                and type(anchor["sourceDirty"]) is bool, "invalid_anchor_source")
        require(type(anchor["taskVersion"]) is str and anchor["taskVersion"] == "release-evidence-quality-v1"
                and type(anchor["datasetHash"]) is str
                and re.fullmatch(r"sha256:[a-f0-9]{64}", anchor["datasetHash"]),
                "invalid_anchor_dataset")
        config = anchor["configuration"]
        summary = anchor["summary"]
        require(isinstance(config, dict) and set(config) == CONFIG_FIELDS
                and all(type(config[k]) is str for k in ("provider", "endpoint", "model", "suite", "reasoningEffort"))
                and all(type(config[k]) is int and config[k] > 0
                        for k in ("contextTokens", "maxOutputTokens", "repetitions"))
                and config["suite"] == "evidence-40-v1", "invalid_anchor_configuration")
        require(isinstance(summary, dict) and set(summary) == SUMMARY_FIELDS
                and all(type(v) is int and v >= 0 for v in summary.values()),
                "invalid_anchor_summary")
        require(summary["requested"] == summary["attempted"] + summary["skipped"]
                and summary["attempted"] == summary["passed"] + summary["failed"]
                and 0 < summary["attempted"] <= 40
                and summary["durationP50Ms"] <= summary["durationP95Ms"],
                "invalid_anchor_coverage")
        require(type(anchor["status"]) is str and type(anchor["pilotOnly"]) is bool,
                "invalid_anchor_mode")
        if anchor["pilotOnly"]:
            require(anchor["id"] == "2026-11-08-distractor05-pilot-success"
                    and anchor["pilotCaseID"] == "distractor-05"
                    and anchor["status"] == "pilot_sample_passed"
                    and summary["attempted"] == 1 and summary["skipped"] == 39,
                    "invalid_anchor_pilot")
        else:
            require(anchor["id"] == "2026-11-07-full-40-historical"
                    and anchor["pilotCaseID"] is None and anchor["status"] == "failed"
                    and summary["attempted"] == 40 and summary["skipped"] == 0
                    and summary["failed"] > 0, "invalid_anchor_full")
    require(seen == EXPECTED_IDS and records[0]["datasetHash"] != records[1]["datasetHash"],
            "invalid_anchor_comparability")
    return records


def verify_evidence(directory, records):
    directory = Path(directory)
    baseline = baseline_module()
    for anchor in records:
        path = directory / anchor["filename"]
        # Hash and validate the SAME captured bytes, not two reads of a path
        # which another process may replace between verification stages.
        try:
            with path.open("rb") as source:
                raw = source.read(baseline.MAX_OUTPUT_BYTES + 1)
        except OSError:
            raise ValueError("missing_or_unreadable_evidence:" + anchor["id"]) from None
        require(len(raw) <= baseline.MAX_OUTPUT_BYTES,
                "oversized_evidence:" + anchor["id"])
        require(hashlib.sha256(raw).hexdigest() == anchor["sha256"],
                "evidence_sha_mismatch:" + anchor["id"])
        try:
            # Keep the same strict report schema as read_report(), but never
            # reopen path after its SHA-256 was checked.
            report = baseline.read_report_bytes(raw)
        except baseline.SafeFailure:
            # This error is intentionally not derived from report content or
            # filenames; the public CLI produces one bounded diagnostic.
            raise ValueError("invalid_evidence_report:" + anchor["id"]) from None
        source = report["source"]
        runner = report["runner"]
        summary = report["summary"]
        require(source.get("revision") == anchor["sourceRevision"]
                and source.get("dirty") is anchor["sourceDirty"], "evidence_source_mismatch:" + anchor["id"])
        require(report["status"] == anchor["status"]
                and runner["taskVersion"] == anchor["taskVersion"]
                and runner["datasetHash"] == anchor["datasetHash"]
                and runner.get("pilotOnly", False) is anchor["pilotOnly"]
                and runner.get("pilotCaseID") == anchor["pilotCaseID"], "evidence_profile_mismatch:" + anchor["id"])
        require(all(report["configuration"].get(k) == value
                    for k, value in anchor["configuration"].items()),
                "evidence_configuration_mismatch:" + anchor["id"])
        require(summary is not None and all(summary.get(k) == value for k, value in
                    anchor["summary"].items() if k not in ("durationP50Ms", "durationP95Ms"))
                and all(summary.get("durationMs", {}).get(k) == anchor["summary"]["durationP" + k[1:] + "Ms"]
                        for k in ("p50", "p95")),
                "evidence_summary_mismatch:" + anchor["id"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=None,
                        help="optional directory of original ignored JSON reports")
    args = parser.parse_args(argv)
    try:
        records = load_anchors()
        if args.evidence_dir is not None:
            verify_evidence(args.evidence_dir, records)
    except (ValueError, OSError, KeyError) as exc:
        print("agent-quality-anchor-check: failed (%s)" % str(exc).split(":")[0], file=sys.stderr)
        return 1
    print("agent-quality-anchor-check: passed (%d public anchors, evidence verified=%s)" %
          (len(records), args.evidence_dir is not None))
    return 0


if __name__ == "__main__":
    sys.exit(main())
