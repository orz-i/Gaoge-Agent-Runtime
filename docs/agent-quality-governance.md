# Task outcomes before feature counts

This first delivery adds three connected pieces: opt-in read-tool exploration,
a recommended bounded tool session, and an offline task-outcome review. It is
independent of PR #131's execution-authority fixes. Neither a scripted pass nor
a successful review command is evidence of real-model quality improvement.

The subsequent [deliverable-acceptance command](../go/agent-runtime-harness/examples/task-agent/ACCEPTANCE.md)
adds one actual code, document and approval task each, with independent Oracles
and an explicitly gated Provider path. Its default remains fixture-only; it is
not a new 40-case score, controlled live A/B result or broad benchmark.

## One offline regression command

```sh
make agent-quality-regression
```

This runs the real Agent/Harness/Tool/Context paths with deterministic fixture
models, without a Provider endpoint or credentials:

| Task/outcome | Acceptance | Additional boundary |
| --- | --- | --- |
| Find evidence after three different no-match queries | Fourth authorized read remains available and produces the final evidence-based answer | Default/legacy suppression remains unchanged; policy survives Runner reconstruction |
| Stop repeating a settled query, then try a different source | Third identical query is refused before execution; changed arguments can complete | JSON key order/whitespace cannot hide the repeat; hard budgets still stop the loop |
| Recover deep evidence from an oversized tool result | Evidence disappears from inline context, is recovered by the scoped original archive reader, then appears in the answer | No fabricated summary or extra read authority |
| Reload/resume a completed tool session | Original Turn/output is returned without new Model/Tool calls | No second transcript or execution loop |
| Refuse invalid composition and unknown outcomes | Unverified model capacity never reaches dispatch; unknown Tool result is not retried | Missing member-only Tool grants, unknown token-metering and foreign Actor controls fail closed |

The named tests are in `agent/tool_progress_policy*_test.go` and
`agent-runtime-harness/tool_agent_session_test.go`. Their purpose is regression
acceptance, not a model leaderboard, general reasoning score or throughput test.

## Review an existing real-model report, without a new run

```sh
python3 scripts/run-agent-baseline.py review-quality PATH_TO_EXISTING_REPORT.json
```

The command reuses the existing bounded reader and strict schema-v1 event/report
validator. It does not modify the report schema, dataset identity, comparison
rules or original report. It emits only validated aggregate numbers and fixed
labels, never endpoint, credentials, source text, Provider response, Run/Turn ID
or arbitrary report extensions.

Read the output in this order:

1. **Task passed / attempted / planned.** A pilot of one passing sample is
   `1 / 1 / 40`, never a complete 100% score. Only complete sampling receives a
   full observed success rate. Failed tasks remain visible.
2. **Task Oracle and recovery separately.** The answer/draft Oracle and complete
   recovery evidence are separate observations. Missing recovery proof on a task
   that failed before interruption is not a failed recovery attempt.
3. **Latency and calls.** These include only observed task samples, not preflight
   requests. They are meaningful only under the original workload/profile.
4. **Usage and cost.** Missing Usage is unknown, not zero. The report has no price
   contract, so tokens are not silently converted into a bill.
5. **Evidence gaps.** Incomplete sampling, dirty/unknown source, missing source
   revision or unavailable dataset identity block baseline-review readiness.
   Readiness means structural review prerequisites only; source labels/hashes
   are self-declared, not authentication, and do not prove the tasks all passed.

Exit code 0 means **the offline review completed**, not that the Agent passed,
the report is independently authenticated, or the candidate is ready to release.
`diagnose-quality` remains the detailed Context/Tool-limit attribution command.
`compare` and `compare-treatment` retain their original matching requirements.

## What this does not establish

The existing `evidence-40-v1` suite still covers **one evidence task family**.
Its `code` subgroup varies release/deployment identifiers; it is not a coding
benchmark. This delivery does not rename those cases, manufacture extra task
families, modify historical private reports or issue paid Provider calls.

The next separately authorized effectiveness experiment should:

- Pin source, workload, exact model/route, effort, tools and all hard budgets.
- Run balanced repeated control/treatment samples and keep held-out tasks.
- Expand the new bounded code/document/approval acceptance tasks into held-out
  workload variants and collect actual-model observations before claiming broad
  Agent quality. One fixture per family is not generalization evidence.
- Keep a controlled same-model/tool track separate from native-product tracks
  using each harness's recommended environment. Never combine their scores.
- Use independently anchored raw reports. A hash in a report is not a signature.

Do not increase budgets, weaken unknown-effect fences or reclassify failures to
make an improvement claim. The policy change needs actual matched live evidence
before it can be described as a real-model uplift.
