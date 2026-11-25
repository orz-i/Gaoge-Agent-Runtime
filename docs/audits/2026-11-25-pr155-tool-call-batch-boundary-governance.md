# PR 155 — Tool Call lifecycle and batch suspension boundary

Base: `28e9c50d`. Local governance after independent audit; not merge admission.

## Unified lifecycle and control boundary

- Capture the actual qualified pending Call return at the consumption boundary, before the Agent State CAS. The original approval witness and pending kind accompany the Call rather than being inferred from its later wait projection. ChildTerminal, ExternalResult, Scheduled and descriptor-less Pending all use the same qualification; unmetered approval-only evidence uses the same existing Call return structure. No new Grant custody classification or SQL schema change.
- Independent Child calls keep the original parallel batch path. A later actual approval-required decision parks a restrictive exact Call digest in Agent State; it grants no execution authority and creates no public checkpoint. The existing Child frontier becomes active and the segment yields. Reconcile every original physical Call before retiring its Grant; consume the resulting receipts in a successor segment, then publish the already required approval without re-running its policy.
- All activation paths use one state transition that moves the original deferred Call and wait into the receipt slot without discarding unsent siblings. The original owned frontier CAS can be retried once on the exact unchanged source or acknowledged by the exact committed successor, with no re-execution of approval or Tool effects.
- Local Tool allowance admission counts unresolved deferred children in addition to consumed calls. Do not enter a new policy when the local allowance is already exhausted; drain existing physical obligations before a business pause. A terminal business failure cannot erase unresolved siblings.
- Preserve native Kernel recovery restrictions, original Tool ledger settlement ownership, independent result observation and the distinction between bounded and observation-only approval custody.

## Local evidence

- Seven previous approval cases plus Scheduled, descriptor-less Pending, ExternalResult and unmetered descriptor-less Pending (11 approval cases); original approval/call execute once and exact receipt and final owner proof survive reopen.
- Six batch-boundary cases: approval approve, approval reject, frontier CAS before-write failure, frontier CAS acknowledgement loss, local allowance exhaustion with two outstanding children, and unrestricted parallel control. Two original children are joined before the next checkpoint/pause; an unsent sibling cannot be reconciled as a completed call. No extra approval-policy evaluation or Tool dispatch, exact ledger usage, exact model-facing result identities and final bounded observation/control.
- The shared suite passed against Memory, native SQLite, GORM, encrypted Kernel/Harness SQLite reopen, and real PostgreSQL with independent Kernel and Harness Ledger schemas/pools (33 leaf cases in the complete shared suite). Core and Harness package tests, selected Race tests, workspace fmt/tidy and core targeted lint passed.
- Initial broad local command exceeded its tool timeout; subsequent completed core and targeted adapter commands are the passing evidence. No source change after verification other than documentation.

## Boundaries

This batch does not fabricate recovery authority for legacy WaitingInput states with outstanding physical children, unknown/opaque callbacks or process death before original callback-return sealing. The allowance matrix covers the local renewable Tool allowance. The separate SharedBudget pre-dispatch reservation-denial/Waiting branch still requires dedicated verification; a budget-created synthetic Pending must not be confused with a physically dispatched provider Pending. The new mixed approval batch is tested through the actual native Agent with trusted owner and durable ledgers; a full Harness continuation-worker mixed-batch run and downstream Gaoge are not newly certified. No commit, push, merge or database migration performed.
