#!/usr/bin/env python3
"""Isolated regression tests for truthful FAIL evidence; never edits the real ledger."""
from __future__ import annotations

import csv
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("ea1_matrix", Path(__file__).with_name("check-issue107-matrix.py"))
assert SPEC is not None and SPEC.loader is not None
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)


class EvidenceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.original_root = checker.ROOT
        self.original_ledger = checker.LEDGER
        self.addCleanup(self.restore)
        checker.ROOT = Path(self.temp.name)
        checker.LEDGER = checker.ROOT / "ledger.csv"
        self.directory = checker.ROOT / "evidence"
        self.directory.mkdir()
        self.sha = "a" * 40
        self.log = self.directory / "failure.log"
        self.log.write_text("SOURCE_SHA=" + self.sha + "\n--- FAIL: TestOriginalUnknown\nFAIL\nEXIT_CODE=1\n")
        (self.directory / "ORIGIN.txt").write_text("SOURCE_SHA=" + self.sha + "\n")
        self.digest()
        self.cell = {
            "cell_id": "EA1-AR-S-P2", "entrypoint": "AR-S", "phase": "P2", "anchor": "G02",
            "inventory_status": "OPEN_COVERAGE", "acceptance_verdict": "FAIL",
            "tested_runtime_sha": self.sha, "tested_host_sha": "",
            "store_backend": "isolated-test", "command": "synthetic unit fixture",
            "evidence_log": "evidence/failure.log", "reviewer_note": "original request replayed",
        }

    def restore(self) -> None:
        checker.ROOT = self.original_root
        checker.LEDGER = self.original_ledger

    def digest(self) -> None:
        digest = hashlib.sha256(self.log.read_bytes()).hexdigest()
        (self.directory / "SHA256SUMS.txt").write_text(digest + "  failure.log\n")

    def test_fail_can_reference_actual_failure(self) -> None:
        checker.verify_local_evidence(self.cell, "FAIL")

    def test_same_failure_cannot_be_pass_or_waiver(self) -> None:
        for verdict in ("PASS", "N_A_APPROVED", "UNRUN"):
            with self.subTest(verdict=verdict), self.assertRaisesRegex(ValueError, "failed test"):
                checker.verify_local_evidence(self.cell, verdict)

    def test_corrupt_fail_hash_is_rejected(self) -> None:
        self.log.write_text(self.log.read_text() + "tampered")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            checker.verify_local_evidence(self.cell, "FAIL")

    def test_fail_requires_exact_origin_sha(self) -> None:
        self.cell["tested_runtime_sha"] = "b" * 40
        with self.assertRaisesRegex(ValueError, "cohort"):
            checker.verify_local_evidence(self.cell, "FAIL")
        self.cell["tested_runtime_sha"] = "not-a-sha"
        with self.assertRaisesRegex(ValueError, "exact Runtime SHA"):
            checker.verify_local_evidence(self.cell, "FAIL")

    def test_data_race_can_be_recorded_but_never_pass(self) -> None:
        self.log.write_text("WARNING: DATA RACE\nEXIT_CODE=1\n")
        self.digest()
        checker.verify_local_evidence(self.cell, "FAIL")
        with self.assertRaisesRegex(ValueError, "failed test"):
            checker.verify_local_evidence(self.cell, "PASS")

    def test_strict_remains_no_go_for_fail(self) -> None:
        with checker.LEDGER.open("w", newline="") as output:
            writer = csv.DictWriter(output, fieldnames=list(self.cell))
            writer.writeheader()
            writer.writerow(self.cell)
        expected = {self.cell["cell_id"]: ("AR-S", "P2", "G02")}
        checker.check_ledger(expected, strict=False)
        with self.assertRaisesRegex(ValueError, "NO-GO"):
            checker.check_ledger(expected, strict=True)


if __name__ == "__main__":
    unittest.main()
