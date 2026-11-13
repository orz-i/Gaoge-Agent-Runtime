# Task-agent example closure plan (read-only diagnosis; implementation not authorized here)

Baseline: e052909. Scope: existing 16 task-agent top-level failures, no Runtime protocol or public self-attestation additions. No tests were run in preparing this plan.

## 1. Composition: one existing first-party adapter, same Host ports

File: go/agent-runtime-harness/examples/task-agent/compose.go.
Replace the pair NewManagedContextPreparer(state.store, manager, resolver) and NewContextRunMiddleware(state.store,nil) with ONE harness.NewBoundedContextAdapter(state.store, manager, resolver, nil).
Keep resolver body exactly: reject any request.Model != config.routedModel(); return ContextTokens=config.contextTokens, ReservedOutputTokens=config.outputTokens, TokenCountContext{Protocol:config.provider,Model:config.modelID}.
Use RunMiddleware: []plugin.RunMiddleware{boundedContext}; PrepareModelRequest: agent.ChainModelRequestPreparers(boundedContext.Prepare). Keep any future schema projections before this final preparer. Do not stack the old preparer/middleware as well.
Preserve outputCeiling, Model client, manager configuration, sealed task ContextPolicy, all tools/limits/timeouts, original RunExecution owner/lease, relation=nil, Harness Context and transcript setup.

Existing implementation evidence: context_bounded_adapter.go Hydrate delegates to the SAME contextRunMiddleware.hydrate (durable ancestry/exclusion/owner checks); Plan still MaterializeContextWindowRequest, loadVerifiedTurn, exact checkpoint/ref, loadSealedConfig, resolver, normalized sealed policy, owner/artifact-reader constraints. Runtime adapter still assesses hard/soft budgets, archives before checkpoint CAS, verifies full artifact/checkpoint values, and binds the committed checkpoint. Only trusted Runtime-owned orchestration records actual hydration/compaction facts after all Next work returns. No caller boolean substitutes for this sequence. Existing context_managed_fault_test.go already switches both hydrate and prepare this way.

## 2. Keep the exact draft receipt-ACK-loss cutpoint

File: examples/task-agent/recovery.go (interruptStore.ApplyExecution and runTask only).
Do NOT move fault to final Model, terminal Mark, or terminal Run. Current fault actually calls backing ApplyExecution successfully, then only on tool.completed with Message=committed returns errInterrupted once. save_draft genuinely appends one non-idempotent draft and returns exact original CallID receipt. Keep fired.CompareAndSwap and preserve this actual committed snapshot/event/source before returning the same sentinel.
With bounded Context, its successful hydration and returned Next are provable even when that Next returns this local Store ACK error. All actual Model callbacks already returned normally. Whole callback completion plus durable known physical state can therefore follow existing Root Mark/Finish and release its internally-owned lease. This is NOT recovery from an arbitrary failed Host layer.
Keep onlyExpectedDraftInterruption strict. Do not allow RootOutcomeUnknown, arbitrary joined errors, classified provider failures, cancellation, timeout, or foreign sentinel wrappers. If bounded configuration still produces Unknown, stop and report evidence rather than widening classification.

## 3. Test owner exclusion while owner is really active, not after legitimate release

Same ApplyExecution cutpoint, AFTER backing successful commit and BEFORE returning errInterrupted: invoke a bounded local observer with original lease and actual returned snapshot. No goroutine/sleep is required: the real original call has not returned, and backing Store transaction lock is already released.
Observer constructs/uses an independent Runtime on the SAME backing Store (not the fault wrapper), loads exact snapshot and original ChildExecutionGrant, verifies original native CheckExecution succeeds, and attempts AcquireExecution with distinct owner and original leaseDuration. At this Running owned-grant cutpoint the exact current Memory contract is ErrChildExecutionBusy, NOT ErrExecutionConflict (memory/execution.go ownedGrant check precedes lease-conflict check). Require empty acquired lease. Check original lease again, full before/after snapshot and grant equality, and complete task counters/committed-execution map equality. Do not dispatch Resume here: it could perform independent recovery and is not a pure native authority probe.
Only a successful set of these checks records LeaseBlocked=true. Propagate an observer failure as a distinct failure alongside interruption, never silently success or fall back to TTL.
After original call returns, remove the old reopened.Resume=>ExecutionConflict requirement and fake clock advance from runTask. Reconstruct both model and all original execution handles exactly as before, with no transcript source, then one actual reopened.Resume performs continuation immediately. The original lease was legally retired; no unknown effect is made safe by time passage. Keep the demo Store clock for existing deterministic construction, but do not use its advance as authority.

Important compatibility distinction: LeaseBlocked field remains a factual owner-exclusion check but its documented observation point changes from post-return lease TTL to actual in-flight owned scope. If the owner requires the old exact post-return conflict behavior, it CANNOT be preserved with the existing correct known-settlement release semantics; that would require a protocol change and is outside this plan. No such change is proposed.

## 4. Preserve the original recovery/oracle guarantees

Keep actual third Model -> save_draft receipt CAS -> ACK fault -> fresh model instance -> fourth Model consuming persisted Tool result -> final answer. Do not precompute final answer, skip fourth Model, duplicate tool send, manufacture completion, or seed state.
Keep full original assertions: two constructed providers; four Model calls for ordinary fixture; one lookup, actual bounded artifact read(s), one draft; original Run and Turn; no extra tools after resume; committed execution IDs/counts unchanged; actual persisted Context used without transcript; exact answer/source/archive/draft oracle; terminal Resume no-op with same Turn revision and counters. HTTP phase/reasoning replay, effort/model selection and capacity/token limits remain unchanged.
Add focused assertions that fault really fired once at the draft event, original receipt snapshot was Running, original physical grant and valid native owner existed during competitor denial, known callback scope is retired after original return, and the continuation uses original Run/Turn rather than a new task. Full durable receipt fields should be compared against original Tool result where existing task capture exposes them, not just a count.
Keep an opaque Context variant regression for the same ACK fault: exact Unknown joined with sentinel, original owned scope retained, no subsequent dispatch even after fixture clock advancement. This negative test must not be conflated with the bounded positive example. Prefer a narrowly configurable example composition helper for this test only; public example default uses bounded adapter.

## 5. Known business-error path remains a real failure, not unknown or success

Existing TestPreInterruptionAgentFailurePreservesOnlyFixedTurnCode must remain unchanged in its semantics: duplicate prior model CallID, ModelCalls=2, ToolCalls=1, LookupCalls=1, DraftWrites=0; TurnFailed and exact agent.tool_invalid safe code; no private provider text leaked. Bounded Context can prove only its own completed hydration/Next lifecycle, permitting original settled Agent failure projection. Do not suppress invalid-tool error, invent a receipt, or treat any terminal status as sufficient completion. If existing pending model state makes knownRootPhysicalSettlement false, report it as a separate unresolved invariant instead of loosening it.

## 6. Fixed file/verification boundary

Implementation files: compose.go; recovery.go; baseline.go; README/documentation lines explicitly claiming TTL recovery (locate those exact lines only). baseline.go current Recovery="in-process-store-clock-expiry" must be replaced by an accurate versioned descriptor such as "in-process-known-receipt-continuation"; recovery oracle version should reflect observation change (v2). Tests: targeted existing main_test.go/recovery_failure_attribution_test.go plus a focused receipt/fence test file; do not rewrite 16 expected failures independently. Update only metadata literal expectations if they explicitly freeze old recovery descriptor.
All 16 original failures must run again on one frozen SHA, including HTTP adapters/effort/model-selection, wrong-answer denominator/pilot no-hidden-retry, 40-case quality and distractor navigation, plus original attribution matrices. Original failure logs remain immutable. Cross-Store EA1 results do not substitute for this example validation.

## Limitations / no invented compatibility

This is in-process reconstruction on Memory Store, not a durable disk restart or provider idempotency claim. Existing fourth-Model continuation and receipt-ACK fault remain the same recovery guarantee. The old wait-for-TTL contract conflicts with lawful immediate retirement of a known whole callback; changing that test's observation point is explicit, not a silent relaxation. The opaque failure test preserves the stronger unknown-does-not-expire guarantee. No new Kernel interfaces, scope proof constructors, schemas, Root/Child recovery protocol, or arbitrary middleware allowlist are required.

Independent bounded review accepted this existing-adapter plan while requiring explicit recovery-oracle observation/version change, opaque Unknown negative, original fourth-Model continuation and original result/no-replay oracle. This is a plan, not evidence of execution.
