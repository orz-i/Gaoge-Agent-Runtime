# Issue 107 EA-1 batch 11: Root admission and whole callback settlement

Frozen source: `8fc23aae3b5719486d1bc25ed7b8455f01e4ab05`. This is the same B11 closure, not a new matrix or an expanded acceptance claim. Exactly EA1-AR-S-P0/P1/P2 change from the preserved B09 FAIL to PASS. The ledger is **75 PASS / 69 UNRUN**. All other 141 rows are byte-identical, including the original 72 PASS source SHAs. Whole EA-1 / WP-RUNTIME remains **NO-GO**; remaining cells and final common-source recertification are outstanding. WP-GAOGE remains independently blocked. No remote PR-head checks are claimed under the no-push/PR/merge instruction.

## What was closed

Root retains its native ExecutionLease, ModelInvocation and ToolDispatch authority. An original-lease-bound physical scope now covers the whole actual callback, including all entered Next calls and post-Next work. It does not replace Root execution with Workflow semantics. Optional RunExecution configuration now supplies internal native authority; unsupported Store capabilities fail closed. Caller-owned leases are reused without acquiring, renewing or releasing them.

P0 verifies actual Start source/preflight/duplicate rejection, losing a competing original claim, and Cancel winning before owned admission, all before Host/Model/Tool entry. The deterministic same-external-lease race keeps the admitted winner inside Host until the loser is rejected; no fallback may borrow another grant ID. P1 begins at the atomic owned-admission boundary, not bare lease acquisition. Competing control and writes preserve the full original Run snapshot, lease identity/epoch and grant. P2 actually sends Host work or loses Model/Tool receipt persistence and proves no replay after original lease expiry and fresh ownership/reopen. A clean state, terminal status, generic error or elapsed TTL never proves a callback completed.

An immutable, exact original callback-return fact and the settlement marker are separate. Actual return can be recorded after lease expiry without granting ordinary mutation or dispatch authority. Mark/Finish and receipt recovery remain original-source CAS operations. The independent return record cannot be overwritten or recreated after Finish. Known authoritative response evidence under Hostfalse is not consumption, budget advancement, wakeup or permission to finish the Host scope. The recorded-response and billing facts are distinguished.

The explicitly authorized bounded Context, SharedBudget and ToolTimeline adapters now place their concrete operation sequence under Runtime ownership. Context includes actual artifact/checkpoint CAS and readback; Budget uses original reservation/dispatch/requeue/settlement identity. Only their internal actual-lifecycle receipts can close their own layer. Host authorization, persistence and billing remain at their specific operation ports. There is no generic public Completed flag or issuer for arbitrary middleware. Ordinary constructors remain opaque, and the opt-in compatibility boundary is documented. Model and Tool journals retain their immutable chain-entry identity; changed candidate RunID or context cannot silently drop a layer, and swallowed inner errors do not erase it.

## Exact evidence and limits

`evidence/issue107-ea1-b11/` contains sixteen final source-tagged logs, their exact commands, ORIGIN and SHA256 manifests:

- Four module compile checks: PASS.
- Memory, native SQLite, auxiliary GORM-SQLite and real PostgreSQL directed race checks: PASS, no skipped directed tests. PostgreSQL Root admission, default admission, unknown and immutable callback-return suites each actually ran three times, including the added bounded Tool cases.
- Three module lint checks: PASS, zero issues.
- A minimal Harness bounded/ordinary ToolTimeline smoke check: PASS before the final full run.
- Complete Core, PostgreSQL, SQLite and Harness module race checks at this same SHA: all PASS.

Native SQLite v4 unknown recovery closes and reopens the database. PostgreSQL uses actual independent pools/connections. Memory retains Store state; auxiliary GORM admission serializes its SQLite SQL pool and is not independent-connection evidence. Bounded timeline ports in the cross-Store conformance vector are deterministic in-memory Host fixtures; they certify Kernel scope transitions, not cross-process Host storage. The directed unknown suite uses explicit native configuration. Default configuration uses the same native implementation and is separately exercised at admission; no unleased fallback is asserted.

The Runtime API checks its actual expressible original source and ownership. It does not authenticate an external Host caller merely because Actor/Thread values match. Separate Host authorization and frozen-cohort gates remain separate. This batch does not certify every Child, Harness, middleware or control matrix cell from a package-level green result.

## Preserved failures and review

B09's original three failures, B11's initial implementation failures and all later frozen diagnostics remain in their own source-tagged directories. Reports at d3fcf6d, 669940a, 3dfc52f and 3c3c524 retain the real failures and their classifications. Compilation diagnostics and the invalid Root-fingerprint fixture diagnostic are also retained. Nothing is relabeled as having passed at an earlier SHA.

The last fixture corrections were not production relaxations: complete Start verification runs at the actual Create snapshot; after the intentional known-capability guidance insertion, terminal immutable policy/source and the exact original user message are still compared. Root keeps the required empty child-only OriginFingerprint. The ordinary timeline fixture uses its catalog helper's original authorized identity rather than weakening that helper. Current targeted smoke and full regression verify the corrected fixtures reached their intended boundaries.

Independent limited source review rejected the mutable ModelChain RunID detachment in 421a414; 0586a69 closed it and the authorized Tool delta without further specific source findings. Subsequent fixture-only reviews covered 04daa64 and corrected Root applicability at 8fc23aa; the reviewer explicitly corrected the earlier missed applicability issue. Final durable evidence/ledger signoff is recorded separately after checksum inspection, not inferred from source review.

No push, PR, merge, real-data migration, or WP-GAOGE edits occurred. Task-owned cache deletion: zero. Available disk at the final run was about 10 GiB, ending at 9.9 GiB; an observed earlier free-space increase was not caused by this batch and is not reported as our cleanup.
