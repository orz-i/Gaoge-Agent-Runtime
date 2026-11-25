# PR 155 — known-unsent Tool closure and recovery

Base: `371d03b1`. Local governance after independent audit; not merge admission.

## Contract

- Acquire the local Tool permit and recheck execution ownership before reserving shared capacity. Commit physical intent immediately before first ToolChain entry. No timeout/permit wait or live observer callback sits between acknowledged intent and that entry.
- Persist an exact Call-bound `toolAdmission.unsent` disposition before terminal release or resumable park. A durable permanent quota denial retains its precise failure code and cannot be turned into a new dispatch; a caller interruption does not itself cancel the Run.
- Close only the exact original reservation at an exact ledger revision, with no dispatch, no receipt and zero usage. Release is terminal; park removes the active slot and permits fresh admission of the same Call. An elected budget cancellation can tighten a park to release, never resurrect a released reservation. Repeated closure consumes the same durable fact, not a new Tool outcome.
- The original native Root frame records no-entry proof before attempting the intent CAS, and removes it before actual ToolChain entry. When ACK disambiguation and cleanup reads both fail after the intent committed, the original returned Grant can seal `OwnedToolReturn.Unsent` in its existing Call return evidence. Receipt-only recovery verifies the exact return snapshot, original ledger/Call, current Grant and ledger revision, then parks under that proof before clearing the marker. A stale observer cannot park a successor reservation. No provider lookup, synthetic Tool receipt or new custody.
- Earlier physical siblings remain fenced; their receipts are reconciled before the unsent disposition is consumed. Exact native nonterminal no-send returns use the same acknowledged-return qualification for original parented Agent segments; arbitrary middleware errors cannot certify this fact.

## Local verification

- Reproduce both preceding P1 failures: ledger Release succeeds before terminal Agent CAS failure; caller cancellation at the pre-entry observer boundary. The new state survives reopen, preserves the permanent denial and leaves no physical dispatch for the interrupted Call. Re-admission does not repeat approval or query a result verifier.
- Ten shared unsent cases: terminal CAS fault, Release before-write fault, Release ACK loss, cancellation before chain, repeated requeue CAS fault, repeated dispatch-intent before-write fault, committed dispatch-intent with both reads lost (metered and unmetered), reserve projection fault and actual chain-entry error as a negative control.
- Mixed sibling case: the unsent third Call parks its reservation while two already dispatched children retain exact original result/approval evidence. Both children settle, then the original unsent disposition is consumed and the third Call is admitted once.
- Root and native parented Agent tests occupy the actual shared local semaphore and cancel the waiting Call. No shared capacity or physical marker is acquired; original ownership can be retired, and the exact original Call later executes once. Ledger unit tests reject mismatched request identities, stale revision and dispatched reservations.
- Complete shared receipt/approval/unsent conformance suite passes Memory, SQLite, GORM, independently reopened encrypted SQLite Kernel/Harness ledger and actual PostgreSQL Kernel/Harness ledger (50 leaf cases). Core/Harness package tests, directed Race, fmt/tidy and targeted lint pass. The initial durable-store test run exposed a closed-observer fixture error, not a production result; corrected fixture and reran the complete durable-store matrix.

## Limits

No SQL schema, second settlement owner, or new custody classification. A live observer ToolStarted event remains non-authoritative. No automatic migration/repair of old unresolved markers without original no-entry evidence. Process death before the original callback return proof is sealed, unknown Host callback effects, and native parented segments without Root receipt-only recovery after irrecoverable marker ACK loss remain fenced. Unknown effects after ToolChain entry are deliberately not refunded or replayed. Production cross-process fault matrices, complete continuation-worker delivery and downstream Gaoge integration remain to be independently validated. No commit, push, merge or deployment performed in this governance batch.
