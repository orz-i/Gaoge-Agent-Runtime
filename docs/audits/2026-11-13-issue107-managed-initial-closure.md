# Managed implementation initial bounded closure

SOURCE_SHA=f2187814eff825f3103e04f4d4f655f6923b21d1

All six compile gates pass. Core, Memory, native SQLite and real PG RESUME-directed smoke fails; Host Memory/PG unissued-proof negatives pass. Original process recovery and RESUME/CANCEL/opaque process smoke fail. Every exact log/exit is preserved in issue107-ea1-managed-initial. No source edits occurred during this single run; no full regression or acceptance is claimed.

Independent reviewer source findings and original authors' read-only classifications form ONE correction set:
1. Fix configured Host storage authority at Runtime construction, no execution-time setter or per-call Host interface; Harness verifies its actual configured Store. Current per-call fake Host can self-report namespace and mint a selection proof. Preserve trusted-Store configuration boundary, do not introduce a generic middleware issuer.
2. Replace pre-Leave configurable IDSource calls with fixed internal entropy. No configured callback before successful Leave ACK; unknown ACK stops.
3. Preserve nil/full fields in managed cloning and optional RawMessage persistence. JSON roundtrip changed zero RESUME cancellation payload into nonnil null, breaking strict Valid and exact ACK identity across three Stores and HostPG. Fix codec/clone, not validation/equality.
4. PG recovery updates must use UpdateColumns semantics so UpdatedAt is not silently modified without a Run revision.
5. Memory child admission must reject a PARKED ancestor under the same lock; ENABLE must preserve/check descendant index rather than overwrite it.
6. New five-field managed completion payload is currently rejected by old one-key boundary parser before its managed branch. Add an exact dedicated five-key format, with unchanged strict old format and duplicate/unknown-key rejection. No ordinary Append may forge the managed completion authority.
7. Opaque process negative never reaches Agent Model recovery: existing PG CheckExecution(nil) sees original owned grant and returns ChildExecutionBusy; native fallback preserves HarnessConflict. Assert that exact prior boundary with full snapshot/grant/epoch and zero new send. Do not alter fallback or claim ModelUnknown was dynamically reached.

Preserve original os.Exit after second pending-intent commit and original no-replay/frozen request/result/receipt/budget assertions. Owner/renew/claim lower tests currently check serialized winner order; only ENABLE uses actual dual-goroutine competition. Do not label sequential cases deterministic concurrent races. Add missing real concurrency in the same fixed authority vectors before final acceptance. The review was bounded and read-only; NOT READY until one corrected source freeze is tested and independently reviewed.
