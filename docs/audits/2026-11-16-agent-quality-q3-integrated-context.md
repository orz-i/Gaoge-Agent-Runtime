# Q3 — Integrated Context-pressure and Agent-quality governance

- Base: `main@5d04c65b611dda7a1e70eafccb8f6ee88d9f979a` (merged PR #114).
- Branch: `governance/agent-quality-q3-20261116` in an isolated worktree; PR Head status is verified separately.
- Goal: close Q3 as **one bounded end-to-end change**: existing real evidence → Context admission decision → trustworthy failure receipt → Agent quality report → offline root-cause analysis → local stress verification. Q3-A1's original offline diagnostic remains the first commit in the same branch, **not a separate review/approval workflow**.
- The work does **not** change input/Tool/Model limits, Native Count protocol, signed Provider replay, persisted checkpoint/Tool receipts, Context compaction algorithm or authorization. It does not issue billable Provider calls.

## Diagnosis grounded in the original reports

The original official 2026-11-07 40-case benchmark attempted 40 and passed 37, with two local `context_budget_exceeded` failures and one earlier ambiguous failure; it was on an older dirty-source profile. A later failed `distractor-05` pilot made 11 model calls, executed the 10-Tool limit with 9 Artifact reads, and received trusted `agent.tool_limit`; native counting was accepted 12/12 with 11/11 same-input Usage agreements. A subsequent clean official single-case pilot passed the original draft/Oracle/recovery checks in four Model and three Tool calls under the same Host Context cap of 32,768. These are **different source revisions/profiles**, not a new controlled full-score improvement.

The two operator-owned original public-anchor reports were independently verified read-only with SHA-256 and the strict report protocol. Missing historical per-request measurements remain unknown; no old report is reclassified or rewritten.

## Q3 integrated implementation

1. **Budget decision stays authoritative.** `agent.BoundedContextAdapter.Prepare` continues to calculate the final request's Context `Assessment`, attempt the same safe compaction candidates and refuse dispatch when the hard Token/serialized-byte limits cannot be met. On its two existing hard-rejection exits it now returns a typed `BoundedContextBudgetError` wrapping the same `ErrBudgetExceeded`. It contains **only** numeric `Assessment`, the configured maximum serialized bytes and one fixed decision: `no_artifact_reader`, `not_owner`, `no_safe_compaction`. The rejected request, raw source, Provider Replay, Tool arguments, model identity and credentials are absent. No Count() call is added.
2. **Original rejected-request evidence reaches the Host report.** `task-agent` extracts this typed error only when the original task failure is actually classified `context_budget_exceeded`, then emits optional `contextPressure` with Hard/Soft input limits, Hard Token estimate, serialized bytes, byte limit, exact/estimated counter origin and the fixed decision. This is *not* a Provider billing receipt or a permission to retry. Existing source/model instruction hashes, Tool/Model limits and default Host policy are unchanged.
3. **Strict report contract and offline diagnosis.** The existing Python protocol accepts this optional evidence only on a Context-rejected sample with sane typed numeric values consistent with the frozen Host input/service cap and serialized-byte policy. Historical reports remain readable without the field. `diagnose-quality` (introduced by Q3-A1) now shows observed per-rejection limits/source/decision when available, distinguishes the trusted `agent.tool_limit` from Context rejection, preserves unknown historical measurements and rejects malformed/fabricated diagnostic fields.
4. **Strategy integration instead of a second runtime controller.** The Q2 `quality` / opt-in `quality-generic` Host instructions, retrieval receipts and `compare-treatment` already provide the controlled strategy axis. Q3 does not introduce an intent router, separate adaptive compactor, auto-budget escalation or a competing execution loop. The existing exact Artifact reader, source verification, Tool receipt and recovery tests remain the accepted behavior. Any later genuine Provider A/B must be same-workload/same-configuration and separately authorized; a changed Host budget would require a separately identified profile, not a falsified `compare-treatment` result.

## Demonstrated trade-off, not a theoretical budget increase

The deterministic Core regression uses the *same* long text request with the same tiny Host input limit. With no native counter, the conservative hard-byte fallback correctly rejects it; with a positive exact Count within the limit, the request is admitted after **one** Count call, without changing the Host limit. A complementary exact Count **above** the limit still rejects it. This proves the current Count boundary can improve usable Context when it has trustworthy native evidence, while retaining fail-closed fallback.

A real `task-agent` HTTP fixture with an oversized synthetic signed reasoning replay still stops before the third physical Model call. Its sample now records the **original rejected-request** Context assessment with `estimated` source and exceeds the unchanged Host hard limit; a smaller signed Replay fixture still succeeds through draft/Oracle/recovery. The opaque replay is not exposed or trimmed, and no extra model/Tool effect is authorized.

## Verification matrix and exit

- Go Core: explicit original-admission positive/negative paths, exact Count vs hard-byte fallback, both non-archive-owner branches, `errors.Is(ErrBudgetExceeded)` compatibility, stable safe error and one Count call.
- Go Task Agent: overlarge encrypted Replay → original two physically dispatched model requests, zero draft, `context_budget_exceeded`, same restricted budget and truthful numeric receipt; smaller replay → normal completion.
- Python: full original report protocol and Q3 negative cases including forged `contextPressure`, mismatched profile/budget, unknown extension data, legacy/pilot/partial reports, contradictory failure causes and no Provider launch in offline CLI.
- Historical original anchors: SHA-256 and protocol checks use operator-owned private reports **read-only**; no files or historical outcomes rewritten.
- Do not claim a new real-model 40-case success rate, Q2 strategy uplift, or external Provider capability beyond verified historical pilot observations.

## Completed local verification

- `go test ./go/agent-runtime/context ./go/agent-runtime/agent ./go/agent-runtime-harness ./go/agent-runtime-harness/examples/task-agent -count=1`: **all four modules PASS**.
- Core fixed-request exact-Count admission vs conservative fallback and numeric budget-rejection tests: **PASS**.
- Task-agent oversized and bounded signed Replay fixture tests: **PASS**, unchanged physical dispatch/draft limits, with the new optional `contextPressure` receipt asserted only on the rejected sample.
- Targeted `go test -race` for the new Core and Task Agent paths: **PASS**.
- `make agent-baseline-test`: **149/149 PASS** (original suite plus Q3 diagnostics/protocol negatives).
- `make agent-quality-calibrate`: **40/40 scripted PASS**, 160 synthetic model calls and 120 Tool calls. This is not an updated real-model score.
- `make fmt-check`, `make go-lint`, targeted Go vet, `git diff --check`: **PASS**.
- `agent_quality_anchors.py --evidence-dir` on the original main worktree: **2/2 PASS**, original private JSON files verified read-only; historical reports were not rewritten.
- Whole-workspace CI and real Provider A/B have **not** been run for this local branch.

## Full-change audit follow-up

The PR-delivery review followed **final request → Context hard admission → original error identity → task-agent report projection → strict Python event validation → offline diagnosis**, including successful execution, local refusal, legacy reports, encrypted replay and recovery fixtures. It confirmed `errors.Is(ErrBudgetExceeded)` continuity, no additional Count/generation/Tool call, no changed execution authorization, no raw input/replay emission, and unchanged normal Path/Q2 hash semantics.

The review added one focused negative boundary to each affected language: **exact Token Count within the budget must still reject when the separate serialized-byte limit is exceeded**, and an optional report carrying that byte-only refusal must validate without falsely claiming a Token overrun. Both tests pass. No new P0/P1/P2 blocker was identified in the scoped changed path. This does not certify external Provider quality or root-cause resolution for old, immutable historical failures.

The previous long local whole-workspace `make go-test` invocation was interrupted by its remote session after several modules passed, so it is not recorded as a whole-workspace pass. The changed Core/task-agent suites and related Harness/Context modules passed in separate local runs; final PR Head required CI remains an independent gate.

The deliverable is an **integrated Q3 PR candidate**, not an automatically merged change. Actual PR Head CI and final merge review are delivery validation, not additional Q3 architectural phases. Existing Q3-A1 diagnostic-only record remains historical provenance of the initial local implementation.
