# Q3-A1 — Context pressure evidence classification (implementation candidate)

- Baseline: `main@5d04c65b611dda7a1e70eafccb8f6ee88d9f979a`, after merged PR #114.
- Scope: **offline, read-only** diagnosis of existing, strictly validated `evidence-40-v1` Agent reports. No Context/Agent/Kernel/Provider implementation, scheduling, compaction, ModelInvocation, persistence, output budget, Tool limit or native Token Count behavior changes.
- Status: first bounded Q3-A governance slice; not Q3 Context optimization approval, a new real-model benchmark, or a release/merge certification.
- Operator-owned ignored reports and the two untracked Provider probe scripts remain unchanged. No paid Provider calls.

## Problem and measured evidence

A `context_budget_exceeded` sample is an authenticated **local Context admission** classification, not proof of a Provider-side HTTP 4xx, model-window exhaustion, a failed native counter, or a particular broken compactor. `agent.tool_limit` is a different condition: with a trustworthy original Harness Turn terminal code, the Agent denied another Tool under its hard limit.

Existing evidence includes:

| Original report | Covered samples | Observations | Interpretation |
| --- | --- | --- | --- |
| 2026-11-07, original complete 40-case report | 40/40, 37 passed | `distractor-05` and `distractor-07` were local `context_budget_exceeded`; `distractor-01` had an older ambiguous wrapper | Two local Context denials, **not** proof of SDK compaction defects. Original source marked dirty and older dataset hash |
| 2026-11-08 earlier `distractor-05` pilot | 1/40 | `agent.tool_limit`, nine successful Artifact reads and zero draft; 12/12 Count accepted, 11/11 matched Usage | Tool budget exhausted; do **not** reclassify as Context pressure |
| 2026-11-08 later clean `distractor-05` pilot | 1/40, passed | Three Tool executions (lookup, exact original-source read, saved draft), all recovery checks passed | The Host navigation strategy can finish under the existing Context cap, but one pilot is not a full benchmark |

Original private reports were inspected **read-only** using the existing bounded strict report loader. The first complete and later clean successful reports have separately pinned public SHA-256 anchors, verified independently before this Q3 analysis. These reports have different revisions, profiles and hashes; **do not compare them numerically as same-profile A/B**.

## Implemented Q3-A1 boundary

`python3 scripts/run-agent-baseline.py diagnose-quality PATH_TO_EXISTING_REPORT.json`

This command:

1. Reuses the strict bounded `read_report()` and `validate_report()` protocol, including sample count/receipt/Usage and frozen suite identity checks. A historical two-case report is not silently promoted into quality evidence.
2. Derives only fixed, content-free output fields: original quality dataset hash, strategy label, clean/unknown source status, observed requested/attempted/complete coverage, fixed failure code, validated case ID, trusted terminal `agent.tool_limit` where present, Artifact-read counts/modes, Count attempt/accepted/unavailable totals and existing matched-input comparison counts.
3. Separately classifies **local Context budget rejection**, **trusted Agent Tool hard limit**, and **other or unknown**. Contradictory budget/Tool terminal claims remain unknown, not whichever cause seems preferable.
4. Represents missing historical Count/read-mode data as **unknown**, not zero. Aggregate `Count()` acceptance does **not** prove the rejected request was counted, or what caused the denial. Unmatched native Count calls are not billable model generations or Usage evidence. Any observed matched-input native undercount is a **separate measurement-safety review signal**, not an inferred task-failure cause.
5. Identifies the remaining missing evidence without inventing it: rejected-request hard/soft token assessment and count source, serialized input size, compaction candidate reasons and possible relief. No raw Tool query, Artifact/source body, Request/Response ID, signed Provider Replay, credentials, endpoint or error text is printed. Report extensions and malformed dataset-hash strings are never echoed; the diagnostic displays only explicitly allowlisted failure codes and well-formed digest identity.
6. Works on pilots, incomplete and preflight-only reports without promoting partial runs to a full 40-case quality score. A successful offline diagnosis exit code means only **report analysis succeeded**, not that the task passed.

The operation is intentionally single-report and observational. It is **not** a new `compare-treatment` or Context-budget A/B comparator. The existing baseline schema, hash creation and `compare`/`compare-treatment` eligibility remain untouched.

## Safety and test matrix

| Invariant | Negative / boundary cases |
| --- | --- |
| No new execution authority | Read-only CLI with `launch` replaced by a failing stub; no API key, Go child or Provider request |
| Trusted taxonomy | Context error vs known `agent.tool_limit`, missing pre-interruption proof, contradictory fixed code and trusted Turn result |
| Strict report identity | Old two-case suite rejected, invalid case ID, impossible Count partitions, invalid fixed terminal code and unsanctioned transport fields rejected |
| Incomplete remains incomplete | Pilot 1/40, no summary, partial coverage; missing Counter metrics remain unknown |
| Privacy | Extra unrecognized Provider transcript material is not included in report-only output; no user/source content leaves the private report |
| No causal shortcut | Count observations are aggregated; per-rejected-request assessment and compaction candidate evidence are explicitly unavailable |

## Next gate — Q3-A2 (not included)

A new, operator-approved, same-profile live Pilot that actually reaches `context_budget_exceeded` may justify **narrow, content-free per-assessment observation**. Before changing an SDK interface, define one authoritative and bounded trace of original final input request identity (opaque digest not published), effective Host/model input limit, exact vs conservative Count source, hard/soft estimate, serialized-byte limit, and compaction candidates/denial classifications. Determine how to observe this without additional Count endpoint calls, raw replay exposure, post-intent mutation or new Model/Tool effects. Existing `Assessment`/`Trace` and Host-level counters should be reused where possible. No callback or observer may change authorization or delay dispatch on failure.

Only after that attribution proves a remaining Context bottleneck may Q3-B propose a separate Host budget experiment or Q3-C propose SDK compaction changes. Such changes need separate workload/treatment/profile identities, deterministic abnormal-input/fault tests, independent final audit and named PR Head CI. Do **not** weaken fail-closed hard budgets, token replay identity, artifact scope, or checkpoint CAS merely to improve a benchmark score.

## Candidate acceptance

Local candidate verification (no remote PR Head):

- `make agent-baseline-test`: **148/148 PASS**, including ten Q3-A1 negative/positive cases (RED against the absent diagnostic API before implementation).
- `go test ./go/agent-runtime-harness/examples/task-agent -count=1`: **PASS**, the original Go execution path has no code changes.
- `make agent-quality-calibrate`: **40/40 scripted PASS** with 160 synthetic Model and 120 Tool invocations; **not real-model quality evidence**.
- `make agent-quality-anchor-check`: **2/2 public anchors PASS**. The separate `--evidence-dir` verifier against the operator-owned primary worktree passed **2/2 original private reports, read-only**.
- Offline `diagnose-quality` was executed against original complete 37/40, failed `distractor-05` tool-limit pilot and successful `distractor-05` pilot; the reported categories matched the documented historical facts.
- `git diff --check`: **PASS**; work on an isolated branch. No paid Provider, whole-workspace test/race/security re-run, actual PR CI or new same-profile A/B score is claimed.

Exit condition for **A1**: content-free, protocol-validated classification and explicit unknowns with no SDK behavior changes. **A2 remains a separate evidence gate**, not an automatic continuation.
