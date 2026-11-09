"""Immutable, content-free historical quality evidence anchors."""
import contextlib
import copy
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import agent_quality_anchors as checker
import test_agent_quality as quality


class QualityEvidenceAnchorTests(unittest.TestCase):
    def test_manifest_is_bounded_and_not_confused_with_new_profile(self):
        anchors = checker.load_anchors()
        self.assertEqual({a["id"] for a in anchors}, checker.EXPECTED_IDS)
        full = next(a for a in anchors if not a["pilotOnly"])
        pilot = next(a for a in anchors if a["pilotOnly"])
        self.assertEqual((full["summary"]["attempted"], full["summary"]["passed"],
                          full["summary"]["failed"]), (40, 37, 3))
        self.assertEqual((pilot["summary"]["attempted"], pilot["summary"]["skipped"]), (1, 39))
        self.assertNotEqual(full["datasetHash"], pilot["datasetHash"])
        self.assertNotEqual(full["configuration"]["reasoningEffort"],
                            pilot["configuration"]["reasoningEffort"])

    def test_manifest_rejects_field_injection_identity_and_profile_confusion(self):
        original = json.loads(checker.MANIFEST.read_text())
        mutations = {
            "unknown": lambda value: value["records"][0].update(apiKey="secret"),
            "duplicate": lambda value: value["records"][1].update(id=value["records"][0]["id"]),
            "path": lambda value: value["records"][0].update(filename="../private.json"),
            "hash": lambda value: value["records"][0].update(sha256="bad"),
            "boolean-int": lambda value: value["records"][0]["summary"].update(passed=True),
            "fake-full": lambda value: value["records"][1].update(pilotOnly=False),
            "same-profile": lambda value: value["records"][1].update(
                datasetHash=value["records"][0]["datasetHash"]),
            "sample-total": lambda value: value["records"][0]["summary"].update(skipped=1),
            "unbounded": lambda value: value["records"][0]["summary"].update(attempted=99),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                candidate = copy.deepcopy(original)
                mutate(candidate)
                path = Path(temp) / "anchors.json"
                path.write_text(json.dumps(candidate))
                with self.assertRaises(ValueError):
                    checker.load_anchors(path)

    def test_strict_report_verification_checks_exact_file_bytes_and_aggregates(self):
        # A synthetic report exercises the verifier without requiring paid
        # credentials or committing historical source evidence to the repo.
        report = quality.quality_report()
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "synthetic.json"
            raw = json.dumps(report, ensure_ascii=False).encode("utf-8")
            path.write_bytes(raw)
            observed = report["summary"]
            anchor = {
                "id": "synthetic",
                "filename": path.name,
                "sha256": hashlib.sha256(raw).hexdigest(),
                "sourceRevision": report["source"]["revision"],
                "sourceDirty": report["source"]["dirty"],
                "taskVersion": report["runner"]["taskVersion"],
                "datasetHash": report["runner"]["datasetHash"],
                "status": report["status"], "pilotOnly": False, "pilotCaseID": None,
                "configuration": {key: report["configuration"][key] for key in
                                  checker.CONFIG_FIELDS if key in report["configuration"]},
                "summary": {key: observed[key] for key in checker.SUMMARY_FIELDS
                            if key in observed},
            }
            anchor["summary"]["durationP50Ms"] = observed["durationMs"]["p50"]
            anchor["summary"]["durationP95Ms"] = observed["durationMs"]["p95"]
            checker.verify_evidence(temp, [anchor])
            altered = copy.deepcopy(anchor)
            altered["summary"]["passed"] -= 1
            with self.assertRaisesRegex(ValueError, "evidence_summary_mismatch"):
                checker.verify_evidence(temp, [altered])
            path.write_bytes(raw + b" ")
            with self.assertRaisesRegex(ValueError, "evidence_sha_mismatch"):
                checker.verify_evidence(temp, [anchor])

    def test_verified_bytes_are_the_only_bytes_parsed_after_file_swap(self):
        # Reproduce the audited TOCTOU: the original SHA-256 is correct, but
        # another writer replaces the file immediately after hashing. The
        # verifier must validate the original snapshot, never the new path.
        report = quality.quality_report()
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "synthetic.json"
            original = json.dumps(report, ensure_ascii=False).encode("utf-8")
            path.write_bytes(original)
            changed = copy.deepcopy(report)
            changed["source"]["revision"] = "b" * 40
            different_bytes = json.dumps(changed, ensure_ascii=False).encode("utf-8")
            self.assertNotEqual(hashlib.sha256(original).digest(),
                                hashlib.sha256(different_bytes).digest())
            observed = report["summary"]
            anchor = {
                "id": "synthetic", "filename": path.name,
                "sha256": hashlib.sha256(original).hexdigest(),
                "sourceRevision": report["source"]["revision"],
                "sourceDirty": report["source"]["dirty"],
                "taskVersion": report["runner"]["taskVersion"],
                "datasetHash": report["runner"]["datasetHash"],
                "status": report["status"],
                "pilotOnly": False, "pilotCaseID": None,
                "configuration": {k: report["configuration"][k] for k in checker.CONFIG_FIELDS
                                  if k in report["configuration"]},
                "summary": {k: observed[k] for k in checker.SUMMARY_FIELDS if k in observed},
            }
            anchor["summary"]["durationP50Ms"] = observed["durationMs"]["p50"]
            anchor["summary"]["durationP95Ms"] = observed["durationMs"]["p95"]
            real_hash = hashlib.sha256
            swapped = False

            def replace_after_hash(body):
                nonlocal swapped
                digest = real_hash(body)
                if body == original and not swapped:
                    swapped = True
                    path.write_bytes(different_bytes)
                return digest

            with mock.patch.object(checker.hashlib, "sha256", side_effect=replace_after_hash):
                checker.verify_evidence(temp, [anchor])
            self.assertTrue(swapped)
            # A later independent verification must reject the altered path.
            with self.assertRaisesRegex(ValueError, "evidence_sha_mismatch"):
                checker.verify_evidence(temp, [anchor])

    def test_malformed_hash_matching_report_is_a_controlled_cli_failure(self):
        for invalid in (b"{}", b'{"schemaVersion": 1, "bad": true}', b"\xff"):
            with self.subTest(invalid=invalid[:20]), tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / "synthetic.json"
                path.write_bytes(invalid)
                anchor = {"id": "synthetic", "filename": path.name,
                          "sha256": hashlib.sha256(invalid).hexdigest()}
                stderr = io.StringIO()
                with mock.patch.object(checker, "load_anchors", return_value=[anchor]), \
                        contextlib.redirect_stderr(stderr):
                    result = checker.main(["--evidence-dir", temp])
                self.assertEqual(result, 1)
                self.assertIn("invalid_evidence_report", stderr.getvalue())
                self.assertNotIn("Traceback", stderr.getvalue())
                self.assertNotIn(str(path), stderr.getvalue())
                self.assertNotIn("private", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
