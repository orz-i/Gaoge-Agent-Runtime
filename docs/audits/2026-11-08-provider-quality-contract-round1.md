# 2026-11-08 — Provider capability / Agent-quality governance, round 1

## Scope and provenance

- Base: `main@f097ebd7919611b0a9cfb110c51d4661906adbf6` (merged PR #98).
- Isolated branch/worktree: `governance/provider-quality-contract-20261108`; no mutation of the main worktree's two untracked owner probe scripts.
- This round executes the first bounded P0 quality/performance evidence and P1 Provider capability contract work. It does **not** introduce a second Agent model loop, intent router, Kernel state, SQL migration, hosted Tools, native Compaction or new paid models.
- Source-of-truth evidence: existing operator-owned ignored Agent baseline reports, not synthetic replacements. No reports were rewritten or committed.

## Observed gap, explicit contract, invariants

The existing OpenAI Responses and Anthropic Messages adapters correctly project ordered User image/PDF blocks but could attempt physical model dispatch for an arbitrary model ID without a positive model-specific modality grant. This differs from the SDK's already default-deny approach for strict Schema, effort and mandatory Tool choice.

1. **Host-only capability authority:** `model.Capabilities.SupportsImageInput` and `SupportsPDFInput` are positive, independent exact-model grants. Unknown/missing/false is denied before HTTP. Provider supports its own media encoder; the flags cannot authorize unsupported referenced audio, uploads, hosted Tools or untrusted source URLs/FileIDs.
2. **Consistent physical paths:** Unary and SSE share `prepare`; OpenAI native input counting must call the same prepare and deny unauthorized media *before* /responses/input_tokens. No permissive fallback or model-name-prefix inference.
3. **Durable binding/recovery:** the selected-model HMAC covers true media grants and changes when they change; an unrelated model remains unaffected. The new flags use `omitempty` when false: previously frozen text-only bindings remain byte-identical. Pending media attempts with a changed model profile stay fenced pending Host resolution.
4. **No identity or accounting changes:** ModelInvocation, ResponseID, original Tool/ProviderReplay bytes, rejection receipts, Usage and Kernel Epoch/CAS are untouched. Neither Count timing nor a positive media grant is an execution receipt.
5. **Historical evidence immutability:** the original complete **37/40 failed-status** run (dirty source) and the current-profile **1/1 distractor-05 pilot pass** (clean source) are stored as two SHA-256 anchored *public summaries*; their different dataset/profile/output/effort configurations are not comparable as same-profile Agent success rates. The raw reports stay ignored.
6. **Non-inferential count timing:** only new, real count downcalls can populate optional `inputCountObservations.totalDurationMs/maxDurationMs`. Include local rejections. Do not infer physical HTTP, model generation/billing, or elapsed-time savings. Missing historical timing remains unknown.

## Invariant × path × abnormal-input matrix

| Invariant | Path | Abnormal / boundary evidence |
| --- | --- | --- |
| Exact positive media grants | Both adapters Unary/SSE | Unknown/unlisted model, no grants, image-only with PDF, PDF-only with image, combined blocks, text-only without profile |
| No alternate permissive route | OpenAI native Count | Unverified image/PDF blocked before HTTP; positively certified media follows native count projection |
| Ordered original media | OpenAI Responses, Anthropic Messages | Existing successful image/PDF User URL and FileID wire fixtures; referenced audio denied |
| Frozen model authority | Provider HMAC | Golden pre-change text-only fingerprint unchanged; changing either media flag rotates binding; unrelated model update isolation |
| Count accuracy remains observational | Context → Count → Agent Tool/Usage | Actual attempts and classified failures, matched/unmatched Usage, private input digest not persisted |
| Bounded telemetry | Go sample → Python report | New optional paired timing fields, negative/Boolean/fraction/too-large/inverted/partial rejection, old report readable |
| Immutable historical results | Public manifest → original JSON | Exact SHA-256 then strict report protocol and source/config/suite/dataset/coverage/latency check; tampered bytes or summary rejected |
| No extra unapproved effects | Offline calibration | 40/40 scripted oracle, no external credentials or model requests |

## Exit criteria and evidence scope

- Provider and Harness/task Agent focused Go tests and Python baseline tests must pass.
- Raw historical anchor validation must pass on locally available original files; CI's static manifest check intentionally does not require private files.
- Go format/tidy/vet and the full required `make check` gate should pass on a *specific* final revision, plus targeted Race and supported external integration where feasible.
- Independent post-change code audit must cover the entire Provider media prepare/binding → ModelInvocation restore/dispatch path and quality report validation, not just changed lines.
- No live paid model benchmark or cross-provider/proxy certification is implied. Existing 37/40 and 1/1 are distinct historical facts; complete current-profile real 40-case quality remains unmeasured.
- Hosted Tools, native Compaction/cache control and major Harness simplification remain later gated work packages. This slice creates no compatibility bridge.

## Independent audit and remediation

The subsequent independent first-round audit reproduced two P2 quality
integrity defects (hash-then-reopen report race; impossible native Count
elapsed totals) and one P3 diagnostic defect (unhandled strict report
exception). The implementation is corrected and has RED/GREEN regression
coverage in the separate
[independent-audit remediation record](2026-11-08-provider-quality-independent-remediation.md).
This local correction is **not** an independent final review or merge admission;
post-fix complete CI and PR-Head checks remain separate.

## Current local evidence (pre-delivery)

- Provider module unit tests: PASS.
- Targeted Harness task-agent tests: PASS.
- Python Agent report suite: **127 passed**.
- Scripted quality Oracle: **40/40** on the frozen local task, with **160 fake model / 120 fake Tool** calls; not real-model quality.
- Historical evidence: original `20261107T093913Z-12da49b7.json` SHA-256 `562c1115541379df1d027faba6f5e74078fa46cb3dbb32a1c259f0009131db03` and `20261108T073605Z-97c7b1ea.json` SHA-256 `228785b209571def2cf67e50d541eadbb9b36edc3fe1070714c19f7399047f03`, with public facts checked through the strict read-only verifier.
- First full `make check` completed with exit 2 at Go `exhaustive` lint: the new media switch lacked an explicit `ContentText` case. The switch was fixed with explicit text and default-deny unknown cases, and a future-unknown-kind regression test was added; Provider lint subsequently reported 0 issues.
- Second complete local `make check` on the corrected code: **PASS, exit 0** (log retained in ignored `coverage/provider-quality-round1/check2.log`); includes metadata, Agent Python tests, Go format/tidy/vet/unit/race/lint for every module, TypeScript lint/typecheck/46 tests/build, evaluation/benchmark/OTel smoke, A2A product and beta.24 clean-consumer release checks. Original provider/backend configuration was not changed by this test.
- No separate real PostgreSQL/Redis Docker integration, OpenTelemetry Collector E2E, security scan, external Provider/model/gateway compatibility call or paid 40-case run was performed during this round. These remain unverified and must not be inferred from `make check`.
- Independent authenticated post-change code review, pinned final-commit PR-Head GitHub CI, merge and release are **not yet performed**. This is locally tested implementation evidence, not independent admission or production sign-off.
