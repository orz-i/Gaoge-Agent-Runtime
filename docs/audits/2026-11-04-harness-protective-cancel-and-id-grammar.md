# Harness I1 post-fix independent audit remediation — safe stop and canonical Host IDs

## Scope and evidence

- Audited regression baseline: `10b97c9120c57d62b9230b8d32c56612f8ba90ad` on `governance/harness-agent-minimal-hardcut-20261104`.
- Read-only independent audit evidence: `/private/tmp/gaoge-independent-audit-10b97c9/report.md`, including executable `go test -overlay` differential probes against `25818e8` and `10b97c9`.
- P1: a transient `GetConfigSnapshot` or `GetSession` failure prevented cancellation of an already running, valid Agent Run. On the prior `25818e8` the Kernel Run became `cancelled` even if final presentation returned an error; on `10b97c9`, `Cancel` exited before writing an intent and left the Run `running`.
- P2: `hostDerivedID` matched only the `hs_`/`ht_` prefix and 24-character length, misclassifying legitimate standalone Store IDs with non-hex digest characters, which had worked at `25818e8`.

## Corrected authority semantics

- `loadVerifiedTurnIdentity` is a narrowly scoped **protective stop** proof: exact requested Turn ID, valid immutable Turn shape and (for the canonical Host namespace) `TurnID(SessionID, HostTurn)` derivation. It does **not** require additional Config/Session reads that are unavailable during a stop. Only root `Cancel` and child `CancelInvocation` use it.
- `loadVerifiedTurn` remains the **complete** read-side proof for all new execution, recovery, Context/Tool/Budget authority, approval, Load, Refresh and other Host-visible data: it adds the verified Session and canonical sealed Config association. There is no second persisted state or compatibility fallback.
- Root `Cancel` additionally verifies the frozen top-level Invocation belongs to the requested Turn and has a valid parent/physical reference. Its existing durable cancellation intent, routed Feature ownership, `validateRootRuntimeOwnership` and Kernel CAS checks remain in force before a physical stop. Child `CancelInvocation` verifies the requested Invocation ID/shape and relies on its existing frozen parent/RunRelation ownership checks. Incorrect Store Turn or Invocation responses cannot redirect cancellation to a foreign Run.
- Cancellation can successfully stop the physical Run and still return an error when **final presentation** requires missing Config or Session data; the test asserts physical Kernel state, not the cancellation API error alone. That error is neither a no-effect receipt nor permission to retry an external effect.
- `hostDerivedID` recognizes only exact `hs_` or `ht_` followed by **24 lowercase ASCII hex digits**. The existing standalone Store contract accepts other IDs, including same-length non-hex, uppercase and short prefix lookalikes. The canonical Host hash namespace remains reserved; no schema changes or version bump.

## Regression grid

- Permanent `TestProtectiveCancelKeepsNativeAgentStoppingUnderReadFailure`: Session/Config read outage after an Agent Run is already `running`, with actual `RunStatusCancelled` after `Cancel`.
- Permanent `TestProtectiveChildCancelStillStopsUnderReadFailure`: the same two failure modes while cancelling a native child Team Run; parent stays `running`, only child becomes `cancelled`.
- Permanent `TestProtectiveCancelNeverTargetsForeignTenantRun`: redirect `GetTurn` to another tenant, replace `SessionID` while retaining the same Turn ID, or alter HostTurn. Both original and foreign Runs must stay running on the denied call; a later legitimate stop cancels only the requested owner's Run.
- Permanent `TestStandaloneStoreIdentifiersRemainNonHostAuthorities`: valid standalone Session/Turn IDs with `hs_`/`ht_` plus 24 non-hex characters, uppercase hex and short digest-like IDs remain readable. Existing canonical Host-derived ID mismatch cases continue to return `ErrConflict`.
- All new probes were RED before the code fix; turned GREEN after it. The targeted regression matrix passed multiple `go test -race -count=3` repetitions. Full Harness and PostgreSQL Harness module tests, Vet and Lint (0 issues) passed before local commit.

## Risk and release gates

- The fix does not allow new execution or Host-visible reads through a missing or corrupted Config/Session. Protective cancellation is an operation with narrower *authorization needs*; no implied permission to change Host output, revive a Run, or grant another budget.
- An arbitrary malicious Store which lies about its own durable writes/reads or CAS is outside the Store contract; a valid exact-ID Turn plus frozen Invocation/Run verification remains required for every protective stop.
- Before merge: lock the final SHA, rerun all repository checks, complete actual PR-head CI and independently review the full affected chain. Report any omitted real Provider/gateway probes and incomplete network vulnerability checks explicitly. This local remediation is not itself GitHub CI evidence.
