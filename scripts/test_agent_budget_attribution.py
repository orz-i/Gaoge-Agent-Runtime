"""Regression: output ceilings, Context reservations, and observed usage are distinct facts.

No Provider requests or private real-model reports are used by these tests.
"""
import unittest

from test_agent_baseline import baseline


def failed_task_report(max_output=128000, model_capacity=1050000, service_cap=32768):
    return {
        "configuration": {
            "contextTokens": model_capacity,
            "maxOutputTokens": max_output,
        },
        "runner": {"profile": {"contextPolicy": {"maxInputTokens": service_cap}}},
        "samples": [{
            "caseID": "distractor-05",
            "status": "failed",
            "stage": "evidence",
            "failureCode": "task_did_not_reach_interruption",
            "modelCalls": 7,
            "artifactReads": 6,
            "draftWrites": 0,
            "usage": {"knownCalls": 7, "missingCalls": 0,
                      "known": {"inputTokens": 7902, "outputTokens": 979}},
        }],
        "summary": {"requested": 40, "attempted": 1, "skipped": 39},
        "preflight": {"status": "passed"},
    }


class OutputCeilingAttributionTests(unittest.TestCase):
    def test_actual_pilot_output_ceiling_cannot_explain_context_input_limit(self):
        high = failed_task_report(128000)
        small = failed_task_report(4096)
        first = baseline.task_output_reservation_facts(high)
        second = baseline.task_output_reservation_facts(small)
        self.assertIn("maxOutputTokens=128000 是单次模型输出上限", first)
        self.assertIn("min(1050000 - 128000, 32768)=32768", first)
        self.assertIn("min(1050000 - 4096, 32768)=32768", second)
        for claim in (first, second):
            self.assertIn("非模型必须生成或已消耗", claim)
            self.assertIn("不能证明本次任务为何停止", claim)
        diagnostics = "\n".join(baseline.failure_diagnostics(high))
        self.assertIn("task_did_not_reach_interruption", diagnostics)
        self.assertIn("min(1050000 - 128000, 32768)=32768", diagnostics)
        self.assertIn("不能从 maxOutputTokens 反推失败根因", diagnostics)
        self.assertNotIn("调整输出预算", diagnostics)
        self.assertNotIn("更换输出预算", diagnostics)

    def test_large_reservation_can_change_input_only_when_model_window_is_binding(self):
        changed = baseline.task_output_reservation_facts(failed_task_report(max_output=1018000))
        self.assertIn("min(1050000 - 1018000, 32768)=32000", changed)
        self.assertIn("并非模型必须生成", changed)

    def test_unsupported_metadata_never_invents_budget_facts(self):
        for capacity, maximum, service in (
            (None, 128000, 32768),
            (1050000, True, 32768),
            (1050000, 128000, None),
            (1050000, 128000, -1),
            (1050000, 1050000, 32768),
            (1050000, 2000000, 32768),
        ):
            with self.subTest(capacity=capacity, maximum=maximum, service=service):
                report = failed_task_report(maximum, capacity, service)
                self.assertEqual(baseline.task_output_reservation_facts(report), "")

    def test_success_and_failed_preflight_do_not_present_task_causation(self):
        completed = failed_task_report()
        completed["samples"][0]["status"] = "passed"
        self.assertNotIn("任务预算事实", "\n".join(baseline.failure_diagnostics(completed)))
        preflight = failed_task_report()
        preflight["samples"] = []
        preflight["preflight"] = {"status": "failed", "failureCode": "provider_failed"}
        self.assertNotIn("任务预算事实", "\n".join(baseline.failure_diagnostics(preflight)))

    def test_generic_http_rejection_does_not_claim_output_limit_cause(self):
        text = baseline.failure_advice("preflight", "provider_failed", {
            "status": "failed", "httpStatus": 400, "responseKind": "json",
        })
        self.assertIn("仅当上游明确报告输出上限不支持时", text)
        self.assertNotIn("可先使用默认 1024", text)
        generic = baseline.failure_advice("preflight", "provider_failed", {
            "status": "failed", "httpStatus": 200, "responseKind": "json",
        })
        self.assertIn("只有确认上游限制", generic)
        self.assertNotIn("可先使用输出预算 1024", generic)


if __name__ == "__main__":
    unittest.main()
