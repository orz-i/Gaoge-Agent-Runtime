# B21 bounded Child callback lifetime repair

Confirmed failure: source `2af101dec9a5fbc2e53a8e4c1ab462c6944e3be2`, Memory `Child/P2/model/swallowed-inner`. Actual inner Model middleware returns unknown after real Next, outer middleware swallows the error, and Child incorrectly Mark/Finish retires its original grant. Root preserves unknown for the identical vector because it already records each callback layer. Independent review confirms existing EA-05/EA-06 violation. The frozen ledger records MW-NEXT-P2 FAIL; original log/hash remain under b21-smoke.

## Closed production scope

Only agent/child_physical_grant.go and the three private callsites in agent/agent.go. Reuse internal callbackscope and existing private first-party failure recording. No new public issuer, Store schema, Root authority substitution, TTL release, recovery Mark or provider retry.

Change the private physical callback to accept (context.Context, *Runner), and pass an operation label. Root's ParentRunID-empty early return forwards the incoming ctx and original runner unchanged, preserving its existing scope and flags.

For a Child only, before Begin, JSON-marshal an explicit {Snapshot, Operation} object containing the original loaded admission snapshot and operation; hash with SHA256 hex. A marshal failure exits before admission. After the original successful BeginChildExecution and WithPhysicalGrant, create an independent callbackscope binding exact child RunID, original GrantID, source hash and operation. Child grant has no Root native lease binding: do not invent OwnerID/LeaseID/Epoch. The original grant/source contain the real identity and revision. The hash is internal callback evidence, not a new durable authorization token.

Use the existing runner.boundedCallbacks private allowlist. Allocate a fresh rootCallbackState for this scoped Child runner so existing Model preparation/normalization, Tool and approval failure observations cannot be inherited from or written into a parent. Defer lifecycle.Close. Invoke the physical body with callbackscope.Bind(ctx, lifecycle).

Non-nil physical error still preserves the grant. A nil outer error additionally requires lifecycle.Complete AND no recorded first-party unknown. Only then perform the unchanged latest snapshot load, knownAgentPhysicalSettlement, trusted Mark and Finish. Callback completeness is necessary, not a replacement for actual receipt evidence. Missing any layer return/receipt preserves unknown, including when an outer layer swallowed its error. Panic/async behavior remains the plugin's existing lifetime policy.

## Exact context propagation

| Existing entry | Operation | Actual bound context path |
| --- | --- | --- |
| Start / Prepare | Existing start or prepare variable | childCtx → RunChain.Invoke → nextCtx → continuePreparedRun |
| ResolveApproval | approval | childCtx → RunChain.Invoke → nextCtx → resolveApproval; original preflight unchanged |
| Resume | resume | childCtx → withRunOwner → driveCtx → RunChain.Invoke → nextCtx → resumeRunning |

None of these Child closures may continue capturing rootCtx for physical execution. Nested children get their own original scope; they do not reuse a parent's callback journal. Root does not receive a new Child journal.

## Same-batch fixture closure

The two P0 foreign-target failures are invalid test setup: Kernel Create requires a nonempty Goal. Supply a legitimate independent target Goal, keep candidate/source rejection unchanged. Strengthen P0 create/preflight rejection to prove Run and scope absence plus every ancestor snapshot unchanged. Duplicate/stale admission preserve original complete self/ancestor snapshots and scope/native-authority observations; do not invent an active lease for a retired Prepare. Foreign candidate checks distinguish legitimate current invocation bookkeeping from unrelated target mutation.

## Verification contract

Retain the strict swallowed-inner negative and all original failure logs. Run all 49 fixed middleware vectors and actual default/native Child Start, Prepare, Resume, Approval, bounded first-party callback, panic and asynchronous regressions. Both receipt-known normal completion and unknown quarantine must be proven. Same frozen source across Memory/native SQLite/native PostgreSQL, real independent PG competitors and persistent reopen. Perform the full affected module/race/lint regressions and final complete common-source gates. Review the two-file production delta and exact callback propagation once, then evidence. A required product capability change or enlarged authorization boundary stops this bounded repair for escalation.
