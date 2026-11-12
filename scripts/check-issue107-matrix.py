#!/usr/bin/env python3
"""Validate the frozen Issue #107 EA-1 matrix and its per-cell evidence ledger.

Inventory mode checks the frozen scope WITHOUT pretending existing test files
are successful executions. --strict is a release admission gate: every cell
must be evidenced on exactly one frozen Runtime SHA or approved as N/A.
No third-party packages, network access or database side effects.
"""
from __future__ import annotations

import argparse
import csv
from collections import Counter
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
MATRIX = ROOT / "docs" / "issue107-execution-acceptance-matrix.md"
LEDGER = ROOT / "docs" / "issue107-execution-acceptance-ledger.csv"
PHASES = ("P0", "P1", "P2", "P3", "P4", "P5")
ENTRY_IDS = (
    "AR-S", "AR-R", "AR-A", "AR-C", "AR-Q",
    "AC-S", "AC-R", "AC-A", "AC-C", "AC-Q",
    "WR-S", "WR-R", "WR-W", "WR-C", "WR-Q",
    "WC-S", "WC-R", "WC-W", "WC-C", "WC-Q",
    "H-AC", "MW-NEXT", "H-FROZEN", "S-CTL",
)
TOKEN = re.compile(r"(?:E\d{2}|G\d{2}|B\d{2}|N[12])")
SHA = re.compile(r"[0-9a-f]{40}")


def fail(message: str) -> None:
    raise ValueError(message)


def read_matrix() -> dict[str, tuple[str, str, str]]:
    content = MATRIX.read_text(encoding="utf-8")
    # Test anchors are real source functions, not labels invented by a
    # matrix author. Cross-repository Hosted names are verified separately
    # against the pinned Gaoge cohort rather than a stale SDK submodule.
    for path, symbol in re.findall(r"\`(go/[^\`]+?)::([A-Za-z]\w+)\`", content):
        source = ROOT / path
        if not source.is_file():
            fail(f"missing test/suite source: {path}")
        if not re.search(
            r"\bfunc\s+(?:\([^)]*\)\s+)?" + re.escape(symbol) + r"\s*\(",
            source.read_text(encoding="utf-8"),
        ):
            fail(f"missing test/suite symbol: {path}::{symbol}")
    expected: dict[str, tuple[str, str, str]] = {}
    entries: list[str] = []
    for text in content.splitlines():
        if not text.startswith("| "):
            continue
        cells = [part.strip() for part in text.strip().strip("|").split("|")]
        if len(cells) != 7:
            continue
        entry = cells[0].split(" · ", 1)[0]
        if entry not in ENTRY_IDS:
            continue
        if entry in entries:
            fail(f"duplicate entrypoint {entry}")
        entries.append(entry)
        for phase, anchor in zip(PHASES, cells[1:]):
            if not TOKEN.fullmatch(anchor):
                fail(f"{entry}/{phase}: unknown token {anchor!r}")
            if anchor[0] in "EG" and not re.search(
                r"^\| " + re.escape(anchor) + r" \|", content, re.M
            ):
                fail(f"{entry}/{phase}: missing evidence/gap registry {anchor}")
            expected[f"EA1-{entry}-{phase}"] = (cells[0], phase, anchor)
    if entries != list(ENTRY_IDS):
        fail(f"matrix rows changed without an EA-1 scope amendment: {entries}")
    return expected


def check_ledger(expected: dict[str, tuple[str, str, str]], strict: bool) -> None:
    with LEDGER.open("r", newline="", encoding="utf-8") as source:
        entries = list(csv.DictReader(source))
    if len(entries) != len(expected):
        fail(f"ledger has {len(entries)} cells, expected {len(expected)}")
    seen: set[str] = set()
    outcome: Counter[str] = Counter()
    inventory: Counter[str] = Counter()
    frozen_shas: set[str] = set()
    for cell in entries:
        identifier = cell["cell_id"]
        if identifier in seen or identifier not in expected:
            fail(f"unexpected/duplicate ledger cell: {identifier}")
        seen.add(identifier)
        entry, phase, anchor = expected[identifier]
        if (cell["entrypoint"], cell["phase"], cell["anchor"]) != (entry, phase, anchor):
            fail(f"{identifier}: matrix/ledger changed or reordered")
        expected_inventory = (
            "EVIDENCE_TO_VERIFY" if anchor.startswith("E")
            else "OPEN_COVERAGE" if anchor.startswith("G")
            else "BLOCKED" if anchor.startswith("B")
            else "N_A_REVIEW"
        )
        if cell["inventory_status"] != expected_inventory:
            fail(f"{identifier}: source inventory status mismatch")
        result = cell["acceptance_verdict"]
        if result not in ("UNRUN", "PASS", "FAIL", "N_A_APPROVED"):
            fail(f"{identifier}: invalid result {result!r}")
        if result in ("PASS", "N_A_APPROVED"):
            if not SHA.fullmatch(cell["tested_runtime_sha"]):
                fail(f"{identifier}: approval lacks exact Runtime SHA")
            if not cell["reviewer_note"].strip():
                fail(f"{identifier}: approval lacks specific reviewer rationale")
            if result == "PASS" and not (
                cell["command"].strip() and cell["evidence_log"].strip()
                and cell["store_backend"].strip()
            ):
                fail(f"{identifier}: PASS lacks command, evidence log or Store")
            if result == "N_A_APPROVED" and not anchor.startswith("N"):
                fail(f"{identifier}: only originally N/A cells can be waived")
            if entry.startswith(("H-AC", "H-FROZEN")) and not SHA.fullmatch(
                cell["tested_host_sha"]
            ):
                fail(f"{identifier}: Harness/Host certification lacks exact Host SHA")
            frozen_shas.add(cell["tested_runtime_sha"])
        elif result == "FAIL" and not cell["reviewer_note"].strip():
            fail(f"{identifier}: FAIL needs cause or reproducible trace")
        outcome[result] += 1
        inventory[expected_inventory] += 1
    if seen != expected.keys():
        fail(f"missing cells: {sorted(expected.keys() - seen)}")
    print(
        f"EA-1 inventory: {len(ENTRY_IDS)} entrypoints x {len(PHASES)} phases = "
        f"{len(entries)} cells"
    )
    print("Inventory:", ", ".join(f"{k}={v}" for k, v in sorted(inventory.items())))
    print("Evidence:", ", ".join(f"{k}={v}" for k, v in sorted(outcome.items())))
    if strict and (
        outcome["UNRUN"] or outcome["FAIL"] or len(frozen_shas) != 1
        or any(cell["inventory_status"] == "BLOCKED" for cell in entries)
    ):
        fail("EA-1 NO-GO: unrun/failing cells, inconsistent SHAs or open blocker")
    if strict:
        print(f"EA-1 matrix certified for Runtime {next(iter(frozen_shas))}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--strict", action="store_true",
        help="fail unless every matrix cell is certified on the same exact SHA"
    )
    args = parser.parse_args()
    try:
        check_ledger(read_matrix(), strict=args.strict)
    except (ValueError, OSError, KeyError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
