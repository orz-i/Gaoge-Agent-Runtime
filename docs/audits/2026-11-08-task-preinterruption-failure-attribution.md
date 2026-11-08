# 2026-11-08 — Task pre-interruption failure attribution (narrow Host governance)

**Implementation commit:** `8633c8eaafe38fff8cf7ceaec17818012e117a63` (isolated local branch)

**Independent review correction:** `9195ab8e74973b0c94565a09b074271026fc8cb5` (mixed-error receipt integrity and permanent Race regressions)

## Why this round

The first complete **real** `gpt-5.6-terra` 40-case Agent evidence run recorded 37/40 passes, with three failures among distractor cases. The immutable private report `20261107T093913Z-12da49b7.json` records `distractor-01`: 6 physical task Model calls, 5 archived Artifact reads, 0 saved drafts, stage `evidence`, last observed HTTP 200, and `failureCode=task_did_not_reach_interruption`. The other two failures already had the distinct local `context_budget_exceeded` code. The first code could not show whether the model terminated without the required draft, or a Provider/Agent error prevented reaching the interruption point. The historical report stays unchanged; its actual root cause cannot be inferred from this wrapper alone.

The old code at `recovery.go` wrapped **all** absence of the simulated committed-draft interruption as `task_did_not_reach_interruption`. An independent fresh fake-model test reproduced **two distinct real Agent/Tool/Context paths** with the same public failure code before this change:
- A model read the authorized original Context Artifact and returned schema-valid JSON with **no save_draft**. The Harness Turn completed normally, with 3 Model calls / 2 Tool calls / 1 Artifact read / 0 Draft writes. The old error code incorrectly implied an unspecified failure to interrupt, not an already completed model turn lacking required side effects.
- A model returned an untrusted error on its first invocation; Agent treated it as a typed model failure, but the old wrapper erased that distinction. The public report did not contain the untrusted error body, yet the classification was not actionable.

## Trust boundaries / invariants

| ID | Boundary | Required invariant |
| --- | --- | --- |
| T1 | Provider/Model → Agent → Harness Turn | Distinguish successful early terminal output from an actual failed Model invocation; do not infer HTTP errors from a normal Turn completion |
| T2 | Harness Store/fault fixture → task report | Only the Host's committed-draft interrupt receipt plus the exact `errInterrupted` signal can enter the resume/lease-check stage |
| T3 | Untrusted Provider error / Store detail → public JSON sample/Python report | Emit only fixed Host-owned failure codes. Never serialize raw `Error()`, `Turn.ErrorDetail`, API keys or opaque replay as diagnostics |
| T4 | Context budget / cancellation / upstream classified error → report | Preserve precise existing typed classifications such as `context_budget_exceeded`, `cancelled`, `provider_unsupported`; do not reinterpret an old report |
| T5 | ResponseID, Usage, Tool effect receipts, Run/Turn identity, retry/recovery | None are changed by error attribution. Late/failed tasks retain observed counts and known/unknown usage; no new model attempts or side effects |

## Small, Host-local fix

`recovery.go` now classifies the **original** `session.Prompt` result before attempting any restart. If the trusted Harness Turn is already `completed` without a draft, it emits `task_completed_without_draft`; a typed Agent model failure becomes `task_model_failed_before_interruption`, and an invalid response becomes `task_model_response_invalid_before_interruption`. Other failed, cancelled and structurally inconsistent states have stable fixed codes. Existing higher-priority typed/context/budget/provider errors are still preserved by the shared `failureCode` function through normal error wrapping. This is a report-only Host concern, not a new Agent/Kernel/Context decision or retry mechanism.

A follow-up independent receipt-authority audit also found a mixed-error variant: the typed cancellation, deadline or Provider classification could override `task_interruption_evidence_inconsistent` even when the Host had observed a contradictory committed-draft receipt. A local RED test reproduced six distinct variants, including an `errInterrupted` marker joined with a cancellation or classified error. The fix centralizes one integrity-code precedence check in `failureCode` and requires a *pure*, boundedly unwrapped local interruption signal before proceeding to the recovery path. Auxiliary errors, classified Provider wrappers and unbounded chains cannot turn a mixed result into an authorized recovery. Other real typed Provider/Context errors keep their previous precedence when no receipt inconsistency exists.

The Python launcher now maps these codes to specific safety-conscious advice and explicitly labels the old `task_did_not_reach_interruption` as a **historical ambiguous category**. Historical JSON reports retain their original codes, dataset identities and numeric benchmark outcomes; no reclassification or invented higher success rate.

## Test design

- **RED→GREEN:** real `runTask` with a fake Model returning valid terminal JSON after a genuine Artifact read but before saving a draft; separate first-Model-call failure with untrusted secret-bearing error; verify original Run/Turn IDs, counts, Stage, Usage-unknown and no fabricated interruption.
- **Systematic matrix:** fixed seed `20261108`, 660 independently selected Host Turn/status/typed-cause/committed-receipt combinations, including completed/failed/cancelled/running/paused, nil and typed errors, invalid model responses, Context budget and cancellation. Assert stable code, original error identity through `errors.Is`, and no secret in any serialized public sample.
- **Independent receipt-authority boundary:** six distinct mixed-error cases failed before the review and passed after the fix (cancellation, deadline, classified upstream failure, joined cancellation, joined classified failure and invalid Model response). Permanent tests also cover the direct sentinel, single and nested wrapping, true/false joined errors, classified wrappers and a 40-level pathological chain under Race. An additional independent **1,536-case** fixed-seed error-tree property matrix checks mixed/unmixed interruption signals, committed-receipt state, public error-code precedence and JSON secret isolation (Race PASS; external Go overlay, no source changes). The original typed Provider classification is verified to remain unchanged in unrelated failures.
- **Python boundary:** safe, distinct `failure_advice` for new fixed categories; explicit uncertainty retained for the historical wrapper. No silent "Provider fault" diagnosis when the HTTP evidence only says 200.
- **Unmodified acceptance:** local 40-case scripted Agent quality Oracle, Go task-agent full unit/race, Python report replay/validation, full workspace quality gates. No paid Provider calls, no change to two existing untracked probe scripts.

## Scope and limits

This correction **does not prove what happened** inside the original 2026-11-07 `distractor-01` run, because historical reporting does not retain a typed original failure/terminal reason. Only future evaluations can distinguish these classes. It does **not** solve the other distractor retrieval quality issues; PR #96 independently addresses candidate navigation. PR #95 archives the original real score and must not be overwritten or backfilled. A new real 40-case Provider run requires an explicit independent user budget/authorization.

The example's public failure-code set changes during an **unreleased beta.24** period without a report schema version change or compatibility bridge. Previously saved reports still parse normally, including the original broad failure code; their meaning remains uncertain rather than silently rewritten.

## Verification and delivery (local implementation)

- **RED → GREEN:** genuine Agent/Tool/Context early normal completion and first Model failure both initially returned the same `task_did_not_reach_interruption`. After the patch, the former returns `task_completed_without_draft`, and the latter returns `task_model_failed_before_interruption`, without inventing an interruption or a saved draft. The untrusted synthetic Provider error remains absent from serialized samples. Original red and green logs: `/private/tmp/gaoge-next-gov-task-failure-{red,green}.log`.
- Fixed seed `20261108` classification/state matrix: **660/660 PASS**, including typed Context budget/cancellation precedence, nil errors, terminal statuses, inconsistent committed-fault markers and secret-bearing cause strings. The test preserves `errors.Is` identity and asserts that public JSON contains no untrusted source strings.
- Offline calibrated `evidence-40-v1` scripted Agent/Tool/Context/recovery tasks: **40/40 PASS**, dataset hash `sha256:838df87f9c4d80155059a949cadd048abc9f921e554792382c62dc9e42fefbbf` for the PR #94 `main` baseline. This is an offline mechanism test, not fresh real-model success evidence.
- Full Go task-agent tests and targeted `go test -race` regression matrix: PASS. Python launcher historical/protocol report validation: **63/63 PASS**, with added guidance for new codes and the retained historical wrapper.
- The **original** local real 37/40 report was read through the new Python validator without mutation. Its original `distractor-01` failure category, all three failed cases, attempted/passed counts and file SHA-256 `562c1115541379df1d027faba6f5e74078fa46cb3dbb32a1c259f0009131db03` are unchanged.
- Before independent receipt review, the isolated-working-tree `make check` completed with **EXIT 0**, including all 12 Go modules unit/Race, format/tidy/vet/lint (0 issues), Python, TypeScript, baseline/evaluation/benchmark/OTel/A2A smoke and beta.24 clean consumers. Evidence: `/private/tmp/gaoge-next-gov-task-diagnostics-20261108/full-check-final.log` and `full-check-final-exit.txt`. **This is not claimed as a full-check result for the newer correction commit.**
- Post-review corrected implementation: original six independent receipt-mismatch RED cases become GREEN; permanent explicit leaf/join/depth/Provider-classification tests PASS in Race. A separate 1,536-case seeded fault graph passes in Race. Subsequent `make fmt-check go-lint` passes across all 12 modules (0 issues) after documented `//nolint:errorlint` exceptions for the intentional direct-sentinel/each-child proof, where `errors.Is`/`errors.As` would wrongly accept unrelated joined errors. Complete make check on the replacement HEAD is a separate locked gate and must be verified prior to merge.
- An initial full check caught one exhaustive-switch Lint failure; it was fixed by explicitly enumerating every Harness Turn status. The final complete check was **re-run** after the fix and passed. No external billable Provider request, SQL schema, Agent loop or persistence change was introduced.

This isolated branch is based on `main@c503aca1d21308b2e3ce243d5c70f9fbc2a9047e`. It deliberately does not modify or merge existing PR #95/#96. The initial implementation HEAD was `858feedd0cce3c42b7ac407c831218423b7d0c55`; an additional independent receipt-integrity audit identified and reproduced the mixed-error gap before the final replacement commit. Do not reuse the original SHA's checks as proof of the replacement. GitHub PR-head CI and final independent replacement-HEAD acceptance are separate delivery gates.
