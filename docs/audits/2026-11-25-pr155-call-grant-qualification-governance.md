# PR 155 — Call consumption and physical Grant qualification

Base: `ee1bf5e9`, local governance batch following the independent audit of that commit. Not a merge-admission decision.

## Unified invariants

- The actual executor or native replay records the Call result and error identity. Budget, Timeline and the final Agent consumption boundary qualify the same result; an unrelated persistence error joined to a refusal cannot turn a failed acknowledgement into model-facing correction. Both middleware orders fence further Model calls on failure. Original charges already committed by an inner Budget are retained, not debited again.
- Grant control custody continues to describe the actual executing callback, not a Tool disposition. The rejected draft classification `OwnedToolSettledReturn` is removed. The returned Grant carries a canonical, per-Call collection of outstanding request/ledger bindings and, when settled, the receipt digest. No new control authority follows from these bindings. A held sibling is verified using its original request/reservation and a settled sibling uses its own receipt, never another Call's evidence.
- The original complete callback plus resolution of all physical obligations qualifies retirement. Recovered results are staged and then consumed by the existing Agent loop without invoking the original Tool. A deferred Named Agent remains verifiable as its original scoped execution after retirement and its result is consumable by the parent.
- Native held settlement writes the original-byte receipt codec. Repeated recovery compares exact original bytes for native sealed receipts and retains the old canonical comparison for legacy Harness verifier-owned receipts. This is not a rewrite of historical evidence.
- The still-unreleased SQLite v9 schema adds one empty field to the existing physical Grant row (`callback_tool_returns`), atomically with the existing v8-to-v9 relation watermark migration. PostgreSQL stores the same evidence on the existing record. No second execution table or schema epoch; earlier draft v9 SQLite files do not pass the new exact schema check. Empty legacy bindings never certify additional outcomes.

## Local evidence

- The original seven settled-Tool recovery cases, including original completion, strict/allow-completion rejection, changed/foreign evidence, opaque callback and recovery-CAS acknowledgement loss.
- Mixed Call batch: two held Child calls and one settled rejection; recover each independently after reopen, reject stale revision/foreign ledger before invoking a verifier, retain exactly three original Tool charges and three result messages, no new Model before all obligations resolve. Verify final owned Root history and deferred Root control.
- Timeline matrix: Budget/Timeline in either order, committed/rejected Tool, Timeline terminal acknowledgement success/failure. Failure retains the original physical fence, with no second Model or Tool call. Successful cases complete with original custody.
- These conformance cases passed with Memory, native SQLite, GORM adapter using SQLite, real disposable PostgreSQL 16.16, and encrypted SQLite Kernel plus Harness ledger reopen. The real PostgreSQL conformance case uses a Memory ledger; it does not certify PostgreSQL ledger reopen.
- Actual Harness deferred Named Agent: committed and rejected child receipt-CAS failures both recover, retire original Grant, pass child terminal verification, deliver the same child to the parent, and finish the Turn (one original child Tool dispatch, two aggregate Tool charges, four Model calls).
- Native held receipt replay rejects changed original JSON bytes even when the JSON is semantically identical; exact replay does not double-charge.
- Relevant core and Harness package tests, selected Race tests, SQLite migration checks, workspace formatting/dependency checks and targeted core lint passed. The full required CI and final independent merge audit are still required on the eventual commit.

## Explicit limitations

Unknown or incomplete original callbacks and legacy/non-owned grants remain fenced. This is not proof of recovery after process death before callback sealing, arbitrary approval/opaque custody, live catalog replacement, Redis queue namespace replacement or AOF failure, or downstream Gaoge integration. The legacy verifier-owned settlement compatibility path is retained. No production migration, commit, push or merge was performed in this governance batch.
