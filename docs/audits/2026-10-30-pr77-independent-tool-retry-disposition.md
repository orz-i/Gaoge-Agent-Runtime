# PR #77 independent admission correction — Tool retry / disposition hard cut

- **Date:** 2026-10-30
- **Protected base:** `main@1cd08e21ce225e637a806da807cea50d5ec02f6f`
- **Initial PR Head:** `3598069f34a23a8033ee43c957e58768f3b81dd3` (5 atomic commits)
- **Corrected executable Head:** `03c240172fc7fd4398617d1fde520878caf38a4f`
- **Scope:** physical Tool re-entry after ambiguous retryable errors and
  unknown or false-success Tool receipt dispositions.
- **Policy:** The initial local acceptance tests do not make this corrected
  PR mergeable. Re-audit and verify required GitHub checks for its final SHA.

## P1-A: retryable error duplicated a physical external effect

Earlier core had protected `ToolDispatchID` at the Run revision boundary,
but `executeToolWithPolicy` still retried when a Handler returned
`tools.NewRetryableExecutionError` *without a terminal no-effect receipt*.

The reviewer wrote a new deterministic test
`TestToolPolicyRetryableErrorWithoutNoEffectReceiptNeverReenters` in the
unmodified initial PR Head, with three cases: missing receipt, wrong Call ID,
or a `committed` result wrongly supplied with the error.
The test **failed in all three cases**: the first Handler call performed
an external mutation, returned the typed error, then a second physical
Handler invocation ran and succeeded. The actual observation was
`attempts=2, physical=2, error=nil`.

**Correction:** the Tool execution policy now permits an immediate retry
only when a trusted Handler explicitly returns **the exact original
`ExecutionRequest.Call.ID` and `ReceiptDispositionRejected` no-effect
disposition together with `RetryableExecutionError`**. The Tool Registry
preserves this verified result; any bare error, wrong ID or non-rejected
disposition fails closed and cannot call the Handler again.
The small `tools.RetryableNoEffectResult(callID, err)` helper is purely a
trusted Handler attestation, not a remote exactly-once protocol.
The existing native lease-expiry recovery test was updated to use the
same explicit no-effect proof before exercising a bounded retry, and still
verifies that one remote physical write survives expiry without replay.

If safe retries exhaust with explicit no-effect proof, Agent fails its
Run with a known no-effect Tool failure rather than falsely claiming an
unknown remote write or a successful external result. A shared Harness
budget that has already dispatched and Held the same Call ID still
refuses a second dispatch; no second ledger or automatic quota requeue is
introduced solely to make a safe retry physically possible.

## P1-B: unknown dispositions accepted as successful final receipts

The original `tools.validExecutionResult` checked only that
`Receipt.Disposition` was nonempty. The reviewer wrote
`TestSuccessfulToolReceiptDispositionMustBeKnownTerminalOrPending`
**before changing production code**. It failed for
`provider_unknown`, `external_done`, `failed`, uppercase
`COMMITTED`, and `rejected` with no accompanying error; all
were accepted as valid successful terminal results.

The corrected Toolkit accepts exactly `read`, `committed` or `pending`
on the successful path, and only an original-ID `rejected` when
accompanied by a trusted typed no-effect error. The Tool Registry and
direct Agent success validation share that closed contract.
The Harness shared BudgetMiddleware refuses to settle a successful
unknown or error-free rejected disposition, suspends the original
dispatched reservation, and does **not** grant another physical call.
The new negative test
`TestUnknownSuccessfulToolDispositionPreservesHeldBudget`
checks both unknown and error-free rejected statuses; a second attempt
on the original Held reservation remains blocked. No unknown status
is mapped to a synthetic known terminal outcome.

## Positive/negative and production-state test evidence

- Missing, mismatched and false terminal retry proof: **red on initial
  Head, green after correction**.
- Unknown and error-free rejected success receipt: **red on initial
  Head, green after correction**.
- Explicit original-ID retryable rejected/no-effect receipt: safely
  allows the bounded original-Call retry, including under Native Run
  lease testing.
- Normal `read`, `committed` and `pending` remain supported.
- Original PR #77 negative cases remain valid: unleased Tool receipt
  loss/restart, post-dispatch model-correctable error after remote write,
  trusted read-only COMMIT reconciliation, anonymous/mismatched original
  identities, and shared budget Held re-entry.
- 12 repeated `go test -race` executions of new Agent/Tools/Harness
  positive and negative tests passed on corrected executable Head.

## Boundaries / rollout

1. Host Tool Handlers must never assert `rejected` no-effect after an
   unverified outbound request. The SDK cannot prove a remote write did
   not occur merely because a trusted Host provided that disposition.
2. Error types are **not** physical retry permits. A custom Tool adapter
   returning `NewRetryableExecutionError` without original-ID rejection
   must adopt `RetryableNoEffectResult` only when it knows no remote
   effect occurred, or rely on trusted read-only reconciliation.
3. `Disposition` is now a closed SDK execution contract, not a provider
   string passthrough. Provider-specific statuses belong in data-only Tool
   `Content`, not the durable execution state discriminator.
4. Store Epoch/lease ownership and physically exactly-once execution
   remain separate from Tool intent and receipt verification. An
   unleased Agent is not certified multiworker-safe by this PR.
5. No Kernel status, SQL DDL/Schema version, new effect ledger, generic
   FeatureDriver or compatibility bridge is introduced.
6. Review old in-flight beta.23 Tool operations before upgrading.
   Unknown remote effects must be drained, reconciled, or quarantined,
   never reexecuted or assigned synthetic terminal receipts.

## Independent merge admission boundary

The corrected executable Code Head is recorded above. All Go, TypeScript,
coverage, security, Docker PostgreSQL/Redis and real OTel Collector gates
must be checked against that implementation before merge admission.
The **actual outcomes** and GitHub strict required check matrix belong
in the independent PR #77 audit comment keyed to the final PR Head;
this document is not itself authority to merge or publish a release.

The final audit-only documentation commit may follow the validated code
without changing executable semantics, but GitHub's strict checks must
still pass on the final PR commit SHA. No PR creation, merge or beta.24
tag/release is authorized by local testing alone.
