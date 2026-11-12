# Issue 107: bounded Root admission and settlement proposal

Status: proposal only; production implementation is not approved by this document. Baseline source 2545aa7b6eda95d193ecd549f29f1aecc387d108; B09 evidence 8dfcc8a6a647d1b3037514518d1987110bb74f35. Scope is the frozen Root Agent authority and unknown-outcome obligations, not a new scheduler or broad audit.

## Preserve the existing protocol
ExecutionLease remains native ownership; ModelInvocation remains model intent/receipt identity; ToolDispatch and ToolOutcomeVerifier remain tool unknown-outcome authority. An additional durable physical barrier must not replace these with Workflow settlement semantics. Existing quarantine revokes native authority and permits trusted repair; it is not an active-owner scope and cannot simply be reused.

## Atomic admission
Pure preparation and source freezing precede Create. Native Acquire follows Create. Before any arbitrary Host RunMiddleware, a single Store transaction must verify RunID, revision, nonterminal state, original OwnerID/LeaseID/Epoch and Store-time validity, then establish a durable scope bound to that original authority. Binding separately after a process-local check is insufficient. A narrow owned-physical-admission capability is preferable to changing existing child admission semantics.

Acquire/takeover and ApplyControl must inspect the same durable barrier before changing epoch or state. Active/Unknown survives lease TTL. Original owner renewal retains exact identity checks; an expired owner gets no extra send authority from the barrier. Legal nonnative configurations need a physical scope without pretending to have a lease. Missing required Store capability must fail before Create or callback, with explicit compatibility documentation.

## State and settlement
- Prepared: no callback has been entered; first-party admission/preflight failure may prove no effect.
- Active: original source/operation/attempt and authority durably admitted before callback.
- Unknown: callback may have acted but no authoritative result is durable. Resume does not enter Host, Generate, or dispatch again. TTL, retryable errors and ordinary cancellation are not no-effect proofs.
- Receipt committed / ACK lost: read original receipt and finish consumption/settlement without sending again.
- No-effect confirmed: only a trusted proof bound to the original attempt permits a new attempt.
- Settled: durable settlement and barrier retirement are idempotently recoverable; read state after ACK loss.

Host middleware requires an explicit trustworthy outcome contract. A nil return is not proof that arbitrary asynchronous work is settled; an error is not proof of no effect. This compatibility decision requires review before implementation. Without a receipt-query/settlement capability, an unknown Host outcome must remain blocked rather than fabricated as recoverable.

Model must persist dispatch uncertainty separately from a merely expired claim. Reuse original invocation ID, RequestHash, provider/binding fingerprint, attempt and ModelCompletionAuthority. A trusted, read-only ModelOutcomeVerifier analogous to ToolOutcomeVerifier may return an original authoritative receipt; public calls should select identity, never supply arbitrary proof. Completed receipts may be consumed without Generate. Also inspect and constrain the same-call retry logic in execution_policy.go so timeout cannot bypass unknown gating. Tool's current unknown behavior and exact-call reconciliation must remain intact.

## Finite implementation surface
Agent: agent.go, run_execution.go, model_invocation.go, execution_policy.go, model_completion_authority.go and narrow Root admission/settlement and Host/Model recovery seams. Kernel: execution.go and child_execution.go owned-admission contract/binding. Memory, native SQLite and PostgreSQL: atomic admission, Acquire/takeover and control checks, durable binding representation and schema migration. Child and Workflow contracts must remain unchanged unless a demonstrated shared invariant requires an explicitly reviewed adjustment.

## Unified verification checklist before source freeze
For every admitted callback: exact original Run/source/request/operation/attempt, revision and native owner/lease/epoch. On rejection: zero Host/Model/Tool calls, unchanged self and ancestor snapshots plus lease/epoch. Exercise Create-to-claim and post-Acquire-to-callback races. During Active and Unknown: competing control and TTL takeover rejected without partial mutation. Verify preflight no-effect exits, original committed receipt recovery, receipt ACK loss, explicit no-effect retry and unresolved outcome retention. Exercise real PostgreSQL independent connections and native SQLite actual reopen; label Memory and auxiliary GORM limits honestly. Verify native and supported nonnative cohorts and missing-capability fail-closed behavior. Keep known successful Tool reconciliation, Model receipt consumption and nested Agent/Workflow regressions.

Current ledger cells AR-S-P0/P1/P2 remain FAIL until actual final-source evidence passes. Root Resume/Recovery cells using this same protocol remain obligations, not automatically certified by the fix. Do not alter the matrix, mark unsupported cases N/A by convenience, or relabel old evidence under a new SHA. Run directed cases and affected regression only after one agreed design and complete assertions; final acceptance requires actual unified-source reruns and authorized gates. No push, PR, merge, production migration or WP-GAOGE work.

## Review record
Two bounded read-only design passes independently recommended preserving native protocol plus durable physical authority, and identified Host result semantics and nonnative compatibility as unresolved decisions. No production change or new test process was performed during design. Parent review is pending.

### Independent bounded design review, 2026-11-12
Review of proposal commit 15ac22a07d8ba72512a851298ba73fcf85469efc found the direction reasonable but not implementation-ready. Required technical clarification: scope spans first Host entry through next and middleware return; ordinary Apply, ApplyExecution, Control and Acquire must all enforce the same Store-owned scope, exact self permit and EA03 ancestor rules. Durable admission/receipt/Mark/retirement need explicit original scope/attempt/source CAS identities and idempotent recovery, with no send or write authority inferred from an expired lease or surviving barrier.

Two compatibility decisions remain escalated to the coordinating owner thread: whether the existing synchronous nil middleware return contract constitutes completion and how legacy Host callbacks adapt; and whether supported nonnative configurations retain an independent no-lease authority or are explicitly hard-cut to required native/new Store capability. Neither change is authorized by this proposal or review. Model verifier may remain optional; absence of provider proof preserves unknown. This design does not require implementation of every provider query adapter in this batch.

Operational check: 9.1 GiB available; shared Go cache is 16 GiB but another go-run server process is active. No shared cache was deleted and that process was not interrupted. No production implementation or additional test run followed this review.
