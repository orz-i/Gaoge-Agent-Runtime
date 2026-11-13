# EA08 Root-owned retirement receipt and original-call ACK

Status: bounded implementation direction authorized by parent; DESIGN FOR READINESS, no source edits or tests in this step. Baseline hotfix worktree `/tmp/gaoge-runtime-c79-continuation` HEAD `29b0c504300c8e863e71c6a0cc2b201d52ef0d8c`, original reproduced test source `da341`. Browser worktree/branch is not an input. Only native Root Agent owned grants change. Child Agent, Workflow and generic Finish missing-grant behavior do not become idempotent.

## 1. Exact failure and required outcome

After the actual original callback has returned and committed its genuine settlement Mark, another owner can legitimately Reconcile and Finish that exact grant before the original call reaches Finish. All Stores currently delete the sole Mark row. The original Finish then receives a missing-grant conflict and reports RootOutcomeUnknown; when that call owned its native lease it leaves the live lease behind. A clean Run, public grant ID, expired TTL or absence of a grant is not sufficient repair evidence. The repair must retain the exact fact removed by genuine Finish.

## 2. Durable records; no public proof constructors

Add `OwnedSettlementEvidence` persisted with a successful Root-owned Mark:
- Version=1; full original ChildExecutionGrant; full Store-returned settled Snapshot (Run/state/checkpoint/result/EventHead), settled revision and canonical full-snapshot digest.
- Original native owner/leaseID/epoch, copied from the grant and checked against actual native row inside Mark; lease expiry is observation, not identity.
- Mark origin `OWNER_RETURN` or `RECOVERED_RETURN`; exact first-party Mark purpose and source digest.
- `LeaseDisposition`: `OWNED`, `BORROWED`, or `LEGACY_UNKNOWN`. OWNED/BORROWED requires an actual original whole-callback outcome proof or existing immutable callback-return completion; not inferred from the Run or executor config at recovery time.
- Immutable callback-return source/outcome when present. A successful actual Mark is necessary; this evidence never manufactures one.

Add a separate `OwnedRetirementReceipt`, key = original grant ID, with unique immutable grant+settlement evidence:
- Version=1, evidence above, phase `RETIRED` or `ACKNOWLEDGED`.
- Retirement snapshot is the same full settled aggregate read under Finish lock; no extra Run revision/event.
- ACK sequence/version (0 before ACK, 1 afterward), original ACK source digest, original native disposition (`released_owned`, `preserved_borrowed`, `legacy_no_release`).
- Never replace/reuse an ID for a later grant. Retain receipt after ACK. Evidence follows the original Run’s existing deletion/retention boundary; no new permanent-retention promise and no TTL cleanup framework. Where the Store exposes no Run deletion API, this change does not invent one. Existing deletion implementations must remove dependent evidence in the same original authorized deletion operation.

All nested JSON is deep-copied. Optional absent values use a precise codec preserving absent versus explicit null. Read validates payload against keyed grant/run/revision/phase fields. Corrupt/contradictory records fail closed.

## 3. Mark evidence and existing compatibility

Reuse `RecordOwnedPhysicalCallbackReturn` on the fast-known path too: after actual whole lifecycle completion and full known receipt checks, persist HostCompleted=true and the original LeaseOwned outcome using the existing private original-callback proof, before Mark. Do not create a public caller bool or a new ownership issuer. The existing Mark port validates its exact private settlement permit and persists Mark provenance plus the existing immutable callback-return completion. Both owner Mark and recovered Mark bind the actual Store-returned full settled snapshot. A peer may legitimately recover Mark/Finish immediately after this completion record, before the owner Mark; the owner then uses the exact retirement receipt plus its original callback proof instead of trying to recreate the grant or completion.

`MarkOwnedPhysicalRecovered` keeps its existing meaning: it can mark only from immutable HostCompleted callback-return evidence and known original receipts. The Store derives OWNED/BORROWED from that stored completion, not from a new recovery caller. Mark ACK loss is resolved by reading the genuine committed marker/evidence and continuing Finish; it is never re-created from a clean aggregate.

Existing Root markers written by the old API remain Finish-able: stamp `LEGACY_UNKNOWN` evidence at genuine Finish using the already verified persisted marker and actual aggregate; no lease release is authorized from that legacy marker alone. If an original immutable callback completion is present it can supply the original disposition. Existing Child/Workflow marker layout/validation and missing-Finish contract remain unchanged. Newly executing Root Agent paths always emit the stronger marker before any Finish opportunity.

## 4. Finish transaction and exact receipt persistence

Keep `ChildExecutionStore.FinishChildExecution(ctx, grant) error` behavior: a missing active grant remains ErrExecutionConflict, even if a retirement receipt exists. Do not turn this generic method into an ACK API.

When an existing **Root-owned** grant is legitimately finished, each Store transaction/mutex:
1. Locks original Run/native/grant rows in existing lock order and revalidates original grant, nonzero genuine settled revision, exact full current settled snapshot and all existing managed/authority gates.
2. Validates persisted settlement evidence (or the explicit legacy conversion above). Does not accept any passed receipt/Completed bool.
3. Inserts immutable RETIRED receipt with exact actual Store aggregate and Mark provenance; existing different receipt conflicts and rolls back. Existing identical receipt plus still-active grant is also rejected as an impossible state, not silently normalized.
4. Removes only the exact original grant/index, in that same transaction. It does not release a lease, acquire/renew an owner, mutate Run/state, or emit a wakeup.

An error/ACK loss after commit leaves RETIRED evidence. Any failure before commit leaves both original grant and marker unchanged. Ordinary missing Finish still conflicts; the original call must use the separate private ACK path.

## 5. Narrow ACK API and first ACK transaction

Optional Store capability:
```
LoadOwnedRetirement(ctx, grantID) (OwnedRetirementReceipt, error)
AcknowledgeOwnedRetirement(ctx, originalGrant, OwnedRetirementAcknowledgement) (OwnedRetirementAckResult, error)
```
`OwnedRetirementAcknowledgement` has private fields; validation-only Matches may be exported for SQL adapters. Runtime constructs it only from an internal Agent original-return/Mark proof or the concrete first-party recovery path reading the Store's genuine Mark/retirement evidence. It binds the entire grant, entire settled snapshot, original Mark source, actual original LeaseDisposition and the retirement digest. It cannot be issued from grant absence, clean Run or arbitrary caller-supplied snapshots. A zero/foreign/copied-to-another-grant purpose is rejected. Runtime has no `Release=true` public option.

For a receipt still RETIRED, Store atomically:
- Re-read exact receipt, full current Run aggregate, native epoch/token, and complete self/descendant grant index. Require current aggregate equals the receipt's full settled aggregate; current epoch equals original epoch; no replacement/descendant grant exists. Native token must still have original owner/leaseID/epoch (expiry need not be live: no send or renewal is being authorized).
- Revalidate private ACK proof against stored immutable evidence; distinguish source mismatch, epoch mismatch and foreign grant as ErrExecutionConflict with zero mutation.
- OWNED: clear only that exact native lease while preserving epoch; BORROWED: preserve native token/expiry exactly. LEGACY_UNKNOWN: never clear a lease; only a legacy read/retirement observation is available, not an original-owner ACK that claims release.
- Persist ACKNOWLEDGED+exact original ACK source+disposition atomically with any owned lease clear. No Run/event/wakeup changes. Never release a newer lease and never increment/recreate an epoch.

This atomically solves release-ACK loss. An ordinary ReleaseExecution followed by a separate ACK write would recreate the same uncertainty and is not used here.

## 6. Already-ACKNOWLEDGED retries versus new source/epoch

Define a typed result `{ReceiptID, SettledSourceDigest, PreviouslyAcknowledged bool, Disposition}`. It describes completion of the **old call**, not a current Snapshot or dispatch permit.

- First ACK while RETIRED with changed current source/new epoch/replacement grant: **ErrExecutionConflict**, no release, no ACK write. Old receipt alone does not cross those boundaries.
- Exact repeat after ACKNOWLEDGED: validate immutable receipt and the same original private ACK identity/source/disposition, then return `PreviouslyAcknowledged=true` **read-only**, even if a later legitimate call has advanced Run/epoch. Do not inspect a later epoch to release anything; do not return it as the old call's state or grant new authority. Wrong old proof/source still fails.
- This is deliberately not a loophole in the new-epoch negative: that negative applies to *first* acknowledgement/action. The repeat is a read of an already committed original ACK. Tests must distinguish these cases and prove newer Run/native/grant rows are byte-for-byte unchanged.
- If review requires every historical read to fail after an epoch change, that is a different API outcome and would lose safe recognition of a previously committed ACK; do not silently conflate it with a write authorization rule. This proposal chooses the typed, read-only historical confirmation above.

Agent returns the original callback result only after exact ACK/previous ACK confirmation. It never uses that result to start another segment without fresh ordinary admission. No caller-owned lease is released. Missing receipt, corrupt receipt, RETIRED source mismatch or unknown Mark remains a real failure, not swallowed conflict.

## 7. Reconcile, Mark ACK loss, and managed lineage

Explicit Reconcile of an already genuinely marked scope remains immediately permitted, even while the original owner is alive. It uses the existing complete-receipt checks, completes any original managed-successor bookkeeping through its existing exact source proof, then Finish. It does not wait for a local in-flight guard or lease expiry. When the marker carries the new original disposition, this first-party path performs the same narrow ACK immediately; the racing original call subsequently observes that exact ACK and succeeds without a second release. For old markers with no original disposition, retain existing conservative no-release behavior.

The original settled path replaces both success and Mark-ACK-loss cleanup sequences with: persist real callback-return completion, ensure genuine Mark (or verify the exact peer retirement receipt), ensure existing managed completion, attempt actual Finish if still present, then exact receipt ACK. If Finish fails because a verified competing finisher already produced the exact receipt, ACK may proceed. Arbitrary Finish errors without matching receipt remain failures. Do not broadly catch ErrExecutionConflict or ErrNotFound.

Managed-successor caveat must be explicitly covered, not hidden by the primary plain-root reproducer: existing `CompleteManagedRecovery` expects an active marked grant. Before deleting a Root-owned grant, Finish ONLY verifies any matching original managed-successor lineage is already correctly CLOSED with the exact settled snapshot. Generic Finish never acquires authority to perform T4 or mint a completion proof. No new recovery selection/epoch/dispatch is granted. Thus a direct Store finisher cannot delete evidence required by T4 and strand the original call. This reuses existing original-successor/source matching; it does not generalize managed recovery. Tests include both ordinary and managed-successor Roots. If the implementation cannot reuse that exact transition without expanding authority, stop and report this specific interaction before code.

## 8. Capability and compatibility boundary

Expose the retirement Store methods as a distinct optional interface so legacy Store implementations continue to compile. Root-owned execution requires this capability together with the existing OwnedPhysicalExecutionStore before a new Root callback starts; absent capability returns typed ErrExecutionRequired before Create/admission, never a post-dispatch fallback that drops retirement evidence. All built-in Memory/native SQLite/Postgres adapters implement it; default/non-native RunExecution configuration still selects existing durable native authority, and borrowed native leases remain supported. No default SDK configuration is disabled. Existing custom native Store adapters and capability-hiding wrappers must implement/forward these two narrow methods to run the upgraded Root protocol; this is an explicit SDK adapter contract change, not an invisible generic fallback. Wrappers in tests must preserve the actual capability so tests reach the intended fault boundary. Legacy already-marked Root scopes can still use the existing Reconcile/Finish compatibility path without synthetic ownership/release; new owner ACK guarantees cannot be claimed for an adapter that never stored the receipt. The release note must state this adapter requirement. If retaining unchanged custom adapters without that capability is a mandatory user-visible guarantee, report that concrete incompatibility before implementation rather than silently declaring such adapters safe.

## 9. Memory / SQLite / PostgreSQL schema and tests

Memory: settlement evidence in existing memoryChildGrant plus immutable receipt map keyed grant ID; same authoritative mutex for Mark/Finish/ACK/native release. SQL: add optional evidence payload to existing Root grant row, and a separate retirement table keyed grant ID with original Run ID, settled revision, phase, version and strict payload. No browser attachment/table/logic. PostgreSQL additive migration with existing locks; native SQLite explicit v5→v6 migration, freezing old v5 DDL and refusing unsupported schemas. Browser's independent unpublished v6 must be rebased later, never mixed into this hotfix. Persistent namespace/source remains original; no new external credentials or process-local-only receipt.

Required finite verification (all three Stores, PG independent pools, SQLite all handles closed/reopened):
- Exact reproduced owner-Mark → peer-Reconcile/Finish/ACK → original finish/ACK: original call completes, OWNED lease released once, Tool/Model dispatch unchanged, original checkpoint/receipt/budget unchanged.
- Store Finish commit ACK loss; ACK commit+release ACK loss; genuine Mark commit ACK loss; fresh Reconcile still immediate.
- Borrowed lease retained exactly through Finish/ACK and duplicate ACK.
- First ACK wrong full snapshot/revision/EventHead/owner/lease/epoch, replacement grant, foreign grant/source proof, missing/corrupt/different receipt: reject and full no-mutation.
- Already ACKNOWLEDGED old-call retry after legitimate new epoch/current source/grant: read-only historical result, new authority and aggregate untouched; changed old proof still rejects.
- Unknown/active callback, no real Mark, merely clean/terminal Run, legacy marker without disposition: cannot mint release authority.
- Exactly one retired row and ACK under competing finish/ACK, with real transaction barriers; no duplicate events/releases.
- Managed-successor T4 preserved; Child/Workflow ordinary missing Finish still conflict and existing conformance unchanged.

Implementation changes require one source freeze, narrow independent review, same-SHA Core/SQL/Agent/Harness directed tests and affected full regressions. No public merge/deploy or Browser implementation is part of this design step.

## 10. Readiness closure: T4 and one canonical ACK identity

T4 is not a new reducer or Finish privilege. Owner and explicit Reconcile invoke existing Runtime.CompleteManagedRecovery with the existing exact private settlement proof while the marked grant is still active. Concrete existing implementations are memory/managed_recovery.go:249, sqlite/managed_recovery.go:380, postgres/kernel_managed_recovery.go:323. The Runtime wrapper is kernel/managed_recovery_runtime.go:205. These check the actual original successor grant, original selection/authority, current full settled snapshot and private proof before CLOSED. Finish merely reads all matching successor histories under its existing Run lock/mutex and requires RESUME, original authority, CLOSED, exact Terminal snapshot; no match is an explicit empty closure set. It copies the validated immutable closure identities (safe-point/recovery/selection/generation, successor grant, closed revision, complete Terminal digest) into retirement evidence before deleting the grant. No writes to managed state occur in Finish. If a peer already finished, owner/reconciler first validates exact retirement evidence and its closure set, then ACKs: it must NOT call CompleteManagedRecovery again expecting the deleted active grant. An unfinished matching T4 blocks Finish without mutation. This retains old managed authority and makes every Finish winner prove prior closure.

Canonical ACK identity is digest(version, immutable original grant, settled full snapshot, persisted original callback outcome/disposition, immutable retirement evidence including closure set). OWNER_RETURN and RECOVERED_RETURN designate separate private proof validation entrances only; neither the entrance nor whether this caller performed Mark becomes a distinct ACK identity. A peer recovery ACK and later original-owner ACK of the same evidence produce exactly the same identity. First valid ACK persists this identity atomically with the permitted lease disposition. Every exact later ACK returns the same historical result, read-only; a changed canonical source/proof is rejected. Mark provenance remains the actual winning stored provenance and cannot be rewritten by a later caller.

## 11. Implementation file split (pending readiness)

Core: new kernel/owned_retirement.go for durable types, strict codecs/validity, canonical identity and validation-only private ACK predicate; new kernel/owned_retirement_runtime.go for internal proof-gated wrappers. Existing owned Mark/Finish wrappers preserve generic missing semantics. Memory: memory/child_execution.go captures real Mark evidence and validates prior T4 in Finish; new memory/owned_retirement.go implements receipt persistence and atomic ACK with native lease map; memory/store.go only maps/clone plumbing. Common conformance owns three-Store identical vectors and JSON absent/null cases. SQL author owns original child-grant evidence columns, retirement models/transactions, explicit SQLite v5-to-v6 schema and peer/reopen registrations. Agent author owns real completion-before-Mark and one settlement/retirement/ACK sequence, including exact peer-first branch. No Browser files or schema are inputs. Compatibility of custom adapters is separately under bounded review before declaration freeze; do not add disputed pre-admission rejection yet.

## 12. Independent readiness record

The independent bounded review accepted the T4-only-validation and canonical ACK identity clauses in section 10. This is design readiness, not implementation/test acceptance. The existing four owned Store ports cannot supply durable retirement plus an atomic ACK/native release without a Store protocol implementation upgrade. The three built-in implementations and transparent wrappers can be upgraded; arbitrary old custom implementations cannot be safely adapted outside their transaction. The runtime compatibility policy for such implementations remains pending parent coordination. No disputed pre-admission capability rejection has been implemented.

The negative evidence and original successful-call oracle remain unchanged at commit 29b0c504300c8e863e71c6a0cc2b201d52ef0d8c. Browser production work is isolated and paused.
