"""Cross-boundary Pilot report invariants: admission, evidence, and re-read.

These are offline reports with simulated events; the Go task-agent tests supply
the physical HTTP/Agent execution side of the same interface.
"""

import copy
from pathlib import Path
import random
import tempfile
import unittest

import test_agent_pilot as pilot
import test_agent_quality as quality

baseline = pilot.baseline


def admitted_pilot(context_tokens=16384, service_ceiling=32768, output_tokens=1024):
    config = quality.quality_config()
    config["contextTokens"] = context_tokens
    config["maxOutputTokens"] = output_tokens
    report = baseline.new_report(config, {"revision": "a" * 40, "dirty": False})
    events = copy.deepcopy(pilot.pilot_events(config))
    events[0]["profile"]["contextPolicy"]["maxInputTokens"] = service_ceiling
    for event in events:
        baseline.accept_event(report, event)
    baseline.finish_report(report, 0)
    return report


class PilotReportIntegrityTests(unittest.TestCase):
    def test_model_window_is_not_the_quality_service_ceiling(self):
        # Go EffectiveInputLimit takes min(contextTokens - outputReserve, ceiling).
        # The independent Host admission floor remains at 8192 input tokens.
        with tempfile.TemporaryDirectory() as directory:
            for capacity in (9216, 16384, 32768, 33792, 65536):
                with self.subTest(capacity=capacity):
                    report = admitted_pilot(capacity)
                    config = baseline.validate_configuration(report["configuration"])
                    self.assertEqual(config["contextTokens"], capacity)
                    baseline.validate_report(report)
                    writer = baseline.ReportWriter(Path(directory) / str(capacity))
                    writer.save(report)
                    loaded = baseline.read_report(writer.json_path)
                    self.assertEqual(loaded["status"], "pilot_sample_passed")
                    self.assertEqual(loaded["runner"]["profile"]["contextPolicy"]["maxInputTokens"], 32768)
                    self.assertEqual(loaded["summary"]["attempted"], 1)
                    self.assertEqual(loaded["summary"]["skipped"], 39)
                    self.assertFalse(loaded["summary"]["complete"])
            for bad_capacity in (1024, 8192, 9215):
                with self.subTest(invalid=bad_capacity):
                    config = quality.quality_config()
                    config["contextTokens"] = bad_capacity
                    with self.assertRaisesRegex(baseline.SafeFailure, "insufficient_context_capacity"):
                        baseline.validate_configuration(config)
            # A genuine historical service ceiling below the current floor
            # may still be read, but it may not be reused for a new paid run.
            historical = admitted_pilot(8192, service_ceiling=6144)
            baseline.validate_report(historical)
            with self.assertRaisesRegex(baseline.SafeFailure, "insufficient_context_capacity"):
                baseline.validate_configuration(historical["configuration"])

    def test_seeded_capacity_property_matrix(self):
        rng = random.Random(94094)
        for _ in range(200):
            output_tokens = rng.choice((512, 1024, 4096))
            capacity = rng.randrange(output_tokens + 1, 65537)
            config = quality.quality_config()
            config.update(contextTokens=capacity, maxOutputTokens=output_tokens)
            if capacity - output_tokens < baseline.SERVICE_INPUT_TOKENS:
                with self.assertRaisesRegex(baseline.SafeFailure, "insufficient_context_capacity"):
                    baseline.validate_configuration(config)
            else:
                baseline.validate_configuration(config)
                report = admitted_pilot(capacity, output_tokens=output_tokens)
                baseline.validate_report(report)
                self.assertEqual(report["runner"]["profile"]["contextPolicy"]["maxInputTokens"], 32768)

    def test_endpoint_provenance_validated_at_child_event_and_report_boundaries(self):
        for endpoint in (
            "https://gateway.example/custom/infer",
            "https://gateway.example/other/path",
            "https://EXAMPLE.com:8443/v1/responses",
            "http://127.0.0.1:9000/v1/responses",
            "https://[::1]:443/any",
            "https://bücher.example/v1/responses",
            "https://ⓔxample.com/v1/responses",
            "https://[fe80::1%25en0]:443/v1/responses",
        ):
            with self.subTest(endpoint=endpoint):
                config = quality.quality_config()
                config["endpoint"] = endpoint
                report = baseline.new_report(config, {})
                event = copy.deepcopy(pilot.pilot_events(config)[0])
                # Both hashes and origins are an independent Go output, not
                # properties supplied from the report reader's input.
                expected_origin, expected_hash = baseline.endpoint_identity(endpoint)
                self.assertEqual(event["configuration"]["endpointOrigin"], expected_origin)
                self.assertEqual(event["configuration"]["endpointHash"], expected_hash)
                baseline.accept_event(report, event)
        # These origins were independently verified with Go net/url's
        # url.URL{Scheme, Host}.String(), not generated by Python fixtures.
        self.assertEqual(baseline.endpoint_identity("https://bücher.example/path")[0],
                         "https://b%C3%BCcher.example")
        self.assertEqual(baseline.endpoint_identity("https://ⓔxample.com/path")[0],
                         "https://%E2%93%94xample.com")
        self.assertEqual(baseline.endpoint_identity("https://[fe80::1%25en0]:443/path")[0],
                         "https://[fe80::1%25en0]:443")
        for invalid in ("https://foo%41.example/path", "https://%65xample.com/path",
                        "https://[fe80::1%41en0]/path"):
            with self.subTest(invalid_authority=invalid):
                with self.assertRaisesRegex(baseline.SafeFailure, "invalid_endpoint"):
                    baseline.validate_endpoint("openai", invalid)
        original = admitted_pilot()
        for name, mutate in (
            ("path_hash", lambda r: r["configuration"].update(endpoint="https://gateway.example/forged")),
            ("different_origin", lambda r: r["configuration"].update(endpoint="https://other.example/custom/infer")),
            ("forge_origin", lambda r: r["runner"]["configuration"].update(endpointOrigin="https://other.example")),
            ("forge_hash", lambda r: r["runner"]["configuration"].update(endpointHash="sha256:" + "0" * 64)),
        ):
            with self.subTest(name=name):
                changed = copy.deepcopy(original)
                mutate(changed)
                with self.assertRaisesRegex(baseline.SafeFailure, "configuration_mismatch"):
                    baseline.validate_report(changed)

    def test_sample_and_summary_cross_field_receipts_reject_corrupt_success(self):
        original = admitted_pilot()
        baseline.validate_report(original)
        token_forgery = {"inputTokens":999999999, "outputTokens":5,
                         "cacheReadTokens":0, "cacheWriteTokens":0, "reasoningTokens":0}
        mutations = (
            ("total_duration", lambda r: r["summary"]["durationMs"].update(total=99999999)),
            ("p95_duration", lambda r: r["summary"]["durationMs"].update(p95=0)),
            ("summary_unknown_usage", lambda r: r["summary"]["usage"].update(missingCalls=3, complete=False)),
            ("summary_forged_known", lambda r: r["summary"]["usage"].update(
                knownCalls=4, missingCalls=0, complete=True, known=token_forgery)),
            ("sample_unknown_usage", lambda r: r["samples"][0]["usage"].update(missingCalls=3, complete=False)),
            ("sample_forged_known", lambda r: r["samples"][0]["usage"].update(
                knownCalls=4, missingCalls=0, complete=True, known=token_forgery)),
            ("recovery_false", lambda r: r["samples"][0]["recoveryChecks"].update(sameRun=False)),
            ("success_without_completion", lambda r: r["samples"][0].update(stage="resume")),
            ("model_duration_count", lambda r: r["samples"][0]["modelDurationsMs"].pop()),
            ("tool_execution_count", lambda r: r["samples"][0].update(toolExecutions=[])),
            ("tool_step_count", lambda r: r["samples"][0].update(lookupCalls=4)),
            ("missing_model_receipts", lambda r: r["samples"][0].pop("modelDurationsMs")),
            ("missing_tool_receipts", lambda r: r["samples"][0].pop("toolExecutions")),
            ("missing_tool_count", lambda r: r["samples"][0].pop("lookupCalls")),
            ("missing_context_generation", lambda r: r["samples"][0].pop("contextGeneration")),
            ("missing_run_identity", lambda r: r["samples"][0].pop("runID")),
            ("missing_turn_identity", lambda r: r["samples"][0].pop("turnID")),
            ("success_with_error_code", lambda r: r["samples"][0].update(failureCode="oracle_failed")),
            ("no_lookup_in_success", lambda r: r["samples"][0].update(lookupCalls=0)),
            ("no_read_in_success", lambda r: r["samples"][0].update(artifactReads=0)),
            ("no_draft_in_success", lambda r: r["samples"][0].update(draftWrites=0)),
            ("no_archived_context", lambda r: r["samples"][0].update(contextGeneration=0)),
            ("partial_forged_complete", lambda r: r["summary"].update(complete=True)),
        )
        for name, mutate in mutations:
            with self.subTest(name=name):
                changed = copy.deepcopy(original)
                mutate(changed)
                with self.assertRaises(baseline.SafeFailure):
                    baseline.validate_report(changed)
        # Some calls may legitimately have missing Provider usage; don't
        # mistake a known subtotal for complete usage or a zero charge.
        partial = copy.deepcopy(original)
        known = {"inputTokens":100, "outputTokens":4, "cacheReadTokens":0,
                 "cacheWriteTokens":0, "reasoningTokens":2}
        partial["samples"][0]["usage"] = {"knownCalls":2, "missingCalls":2, "complete":False, "known":known}
        partial["summary"]["usage"] = copy.deepcopy(partial["samples"][0]["usage"])
        baseline.validate_report(partial)
        partial["summary"]["usage"]["known"]["inputTokens"] += 1
        with self.assertRaisesRegex(baseline.SafeFailure, "summary_usage_mismatch"):
            baseline.validate_report(partial)

    def test_mixed_known_unknown_usage_across_samples(self):
        report = pilot.fixture.complete_report()
        known = {"inputTokens":280, "outputTokens":34, "cacheReadTokens":2,
                 "cacheWriteTokens":0, "reasoningTokens":8}
        report["samples"][0]["usage"] = {"knownCalls":4, "missingCalls":0, "complete":True, "known":known}
        report["summary"]["usage"] = {"knownCalls":4, "missingCalls":4, "complete":False, "known":copy.deepcopy(known)}
        baseline.validate_report(report)
        tampered = copy.deepcopy(report)
        tampered["summary"]["usage"]["known"]["reasoningTokens"] += 1
        with self.assertRaisesRegex(baseline.SafeFailure, "summary_usage_mismatch"):
            baseline.validate_report(tampered)

    def test_seeded_pilot_protocol_mutations_fail_closed(self):
        rng = random.Random(94007)
        config = quality.quality_config()
        records = pilot.pilot_events(config)
        mutations = (
            lambda es: es.insert(3, copy.deepcopy(es[2])),
            lambda es: es[2]["sample"].update(caseID="window-01", sampleID="window-01-01"),
            lambda es: es[2]["sample"].update(repetition=2),
            lambda es: es[0].pop("pilotOnly"),
            lambda es: es[0].update(pilotOnly=False),
            lambda es: es[0].update(pilotCaseID="code-02"),
            lambda es: es[-1]["summary"].update(passed=40),
            lambda es: es[-1]["summary"].update(complete=True),
            lambda es: es[-1]["summary"].update(skipped=0),
            lambda es: es[1].update(status="failed", failureCode="provider_failed"),
            lambda es: es.__setitem__(slice(2, 4), [es[3], es[2]]),
            lambda es: es.__setitem__(slice(1, 3), [es[2], es[1]]),
            lambda es: es.insert(2, copy.deepcopy(es[0])),
            lambda es: es.pop(),
            lambda es: es.pop(1),
        )
        for index in range(450):
            events = copy.deepcopy(records)
            choice = index if index < len(mutations) else rng.randrange(len(mutations))
            mutate = mutations[choice]
            mutate(events)
            report = baseline.new_report(config, {})
            try:
                for event in events:
                    baseline.accept_event(report, event)
                baseline.finish_report(report, 0)
            except baseline.SafeFailure:
                continue
            self.assertNotEqual(report["status"], "pilot_sample_passed", index)


if __name__ == "__main__":
    unittest.main()
