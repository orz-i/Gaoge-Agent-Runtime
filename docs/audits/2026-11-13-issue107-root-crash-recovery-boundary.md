# EA-1 Root process-crash boundary clarification

This local-only fixture correction accompanies the bounded B11 repair. It does not change the frozen matrix or authorize deployment, pushes, PRs, or live-data migration.

## Before the original callback-return fact

A persisted Run, ModelInvocation intent, dispatch claim, or even model/tool receipt is not proof that the entire original Host callback and all entered Next calls returned. Process termination before that fact must preserve the original grant, source identity, receipts, and Run snapshot. Elapsed client time, lease TTL, and reuse of an InvocationID are not provider idempotency or no-effect evidence. A process restart must not automatically regenerate an unknown response or manufacture callback completion.

The real PostgreSQL HTTP fixture counts every request as a new physical response. Its previous response-before-receipt expectation of two requests therefore did not prove idempotency. The corrected tests require no additional request and unchanged durable state. SQLite pre-return model/tool-receipt crash cases likewise retain the original pending scope rather than silently promoting receipt persistence to callback completion. The pre-admission Create-only crash remains a legitimate resumable zero-callback case.

## After an actual callback return is durably recorded

New PostgreSQL and SQLite subprocess cases inject a pre-Mark failure only after the actual first-party call returns. The normal Agent callback boundary then issues its own opaque proof and records the original completion through the Store port. The wrapper terminates the process immediately after that successful Record commit. A fresh process/Runtime reads that original record and reconciles Mark/Finish without repeating Model or Tool work or changing the Run snapshot.

The tests do not directly insert completion rows, fabricate returned flags, or infer a completion from a terminal status. The preserved original provider receipt, usage, source, exact grant, and physical-call counts remain assertions.

## Owner lifecycle and control

An explicitly bound caller retains ownership of its lease after a segment returns; the caller must release it or reuse the exact identity. A later process may not take over a still-active caller-owned lease. Default Agent configuration now acquires native authority internally, so nil configuration is not an epoch-zero bypass. In-flight physical scope cancellation is rejected without changing the original Run or grant; verified owner completion remains usable.

Only a current-authority receipt CAS whose revision became stale is classified as ErrConflict. Foreign grant, epoch, callback-purpose, source proof, or Host-completion mismatches remain ErrExecutionConflict. No generic authority failure is made retryable.
