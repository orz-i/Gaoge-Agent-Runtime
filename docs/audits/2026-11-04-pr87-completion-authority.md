# PR #87 completion-authority convergence audit

## Baseline and reproduced defects

Audited `0370c55d717582e7f2d8f40e54688c53fadbe545`, after the previous
exact-ID and added-item fixes. The earlier fixes remain necessary; format
checks at separate boundaries did not preserve a single physical receipt.

The new regressions were run against the baseline before production edits:

1. A named Provider returned the same `physical-1` twice. Middleware replaced
   each response ID with a different valid `replacement-<InvocationID>`.
   The Run completed, executed a local Tool (`effects=1`), and consumed two
   model calls / 44 tokens. Exact but rewritten IDs bypassed deduplication.
2. A typed rejected stream emitted one physical ID and returned another with
   valid Usage. The error early return skipped stream/final reconciliation,
   creating two known rejected receipts / 44 tokens despite contradictory
   physical evidence.
3. Persisted consumed+consumed and consumed+completed invocations with the
   same Provider, binding and physical ID passed per-record state validation.
   The latter could reach the completed-invocation recovery fast path without
   the live duplicate fence.

## One authority per named invocation

Agent now binds completion to one trusted source, either the physical
Provider terminal or a verified settled-budget replay. It records the exact
ID, a cloned Usage value and rejection outcome before downstream middleware
can change them. Ordinary validation can only compare with that source; it
cannot register a fabricated first response. Conflicts are sticky and
concurrent registration/validation is synchronized.

A successful completion may still be rejected by later semantic/accounting
validation. A rejected or unknown failed call cannot be promoted to success.
A second source registration is invalid, including physical+replay mixtures.
Unnamed local models retain their prior optional-receipt contract. Existing
single-use `next`, request narrowing, retries, late-result receipt handling
and cancellation behavior remain covered by the original suite.

`ReplayModelResponseAuthority` is an explicit **trusted Host attestation**
entry point. Its caller must first verify a settled reservation, the original
RequestHash, InvocationID, and decoded receipt. The shared-budget middleware
performs these checks before calling it. Agent additionally checks its frozen
request and applies the same ID, duplicate and outcome fence. Matching request
fields alone cannot prove ledger provenance. This API does not isolate a
malicious Go plugin sharing the process and context.

Streaming compares observed IDs with a supplied final ID before returning any
error. Typed rejection still requires the returned Response's own final ID
and authoritative cumulative Usage; partial stream observations never fill
missing rejected receipts. Concurrent callbacks are serialized, contradictions
remain sticky even if callback errors are ignored, and callbacks after return
cannot publish new events.

Restored state now requires unique physical IDs across completed/consumed
invocations within the same Provider/binding. Different bindings and Providers
remain independent. No schema/version change or cross-Run global index is
introduced; unsafe duplicate legacy Beta receipts fail closed.

## Regression matrix

- Unary and streaming success preserve original IDs and execute the expected
  Tool once; matching typed rejected streams retain one sanitized paid receipt.
- Valid-to-valid ID replacement, repeated physical IDs relabeled per
  invocation, replaced/in-place/erased Usage, swallowed rejection, transport
  failure promoted to success, and no-Provider fabricated success fail closed.
- Rejected stream/final conflicts, padded event IDs, missing final rejected ID
  and missing final rejected Usage cannot mint a known completion.
- Unit tests cover success-to-rejection, `errors.Join` rejection identity,
  late results, original Usage pointer mutation, wrong run/invocation/request
  replay, ordinary validation before any source, duplicate/concurrent source
  registration and sticky concurrent conflicts.
- Shared budget is tested both inside and outside an ID/Usage mutator. If
  mutation occurs before settlement, accounting keeps usage unknown. If the
  real receipt was already settled before an outer middleware mutates delivery,
  the original charge is retained, never refunded or rewritten; Agent rejects
  the corrupted delivery and does not execute Tools.
- Accepted and rejected settled receipts recover after an injected crash
  before Agent receipt persistence without a second Provider call.
- Persisted consumed+consumed and consumed+completed duplicate IDs fail;
  different IDs, Providers, bindings and unnamed local models pass.
- Concurrent stream callbacks, ignored conflicting callback errors and a late
  callback after return have race-enabled regressions. Sequential and concurrent
  double `next` calls still permit only one physical dispatch per invocation.

Focused Agent, harness and both Provider suites pass with `-race -count=1`.
The new authority, stream, recovery and budget matrices also pass
`-race -count=10`. Repository-wide `make check` passes, including lint,
TypeScript, evaluation, benchmark smoke and clean release consumers. Final
format and affected-package vet checks also pass. Exact pushed-head CI remains
a separate gate and must finish before the PR is called ready to merge.

## Scope and residual risks

This is a convergence check of response identity, receipt Usage and completion
outcome across middleware, stream errors, recovery, retries and accounting.
It is not a proof that the entire PR or every upstream protocol is defect-free.
Provider adapters still own detailed payload validation and authoritative
cumulative Usage. Trusted Host middleware can intentionally change ordinary
response content or reject it; this change does not freeze all response fields.
A malicious in-process adapter/plugin is not sandboxed, and a global
cross-Run physical-ID registry remains outside the contract. Existing live
Provider probe scripts were preserved and not run or included in the commit.
