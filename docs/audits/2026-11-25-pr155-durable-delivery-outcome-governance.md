# PR 155 — durable delivery and original Tool outcome consumption

Base: PR worktree HEAD `7b192fe7` before this local governance batch.

## Authority and transition invariants

- Both paged scheduler entry points use the same delivery qualification. A volatile or undeclared queue can accept an idempotent job but cannot advance a durable enqueue watermark. A feature-verified consumed edge can advance its observed watermark independently. A declared durable queue must retain its original namespace, identity, and acknowledged jobs; `RequireDurableQueue` remains available to reject volatile composition.
- A Tool's settled Budget receipt preserves the original terminal disposition and opaque payload, plus correction code, message, blocked keys and completion policy for a proven no-effect refusal. Incomplete accounting or projection is not a qualified rejection. Historical adapter receipts remain readable, but do not create new native callback custody.
- The original owned Root callback seals its returned snapshot, exact ledger scope and terminal receipt digest only after the bounded callback chain and accounting have acknowledged the original result. Recovery selects that sealed result, validates the frozen request and reservation, stages the receipt using the existing owned recovery CAS and retires the original grant. A later normal Resume consumes it without invoking Tool middleware or the physical Tool. No new control permission is created by the new custody classification.
- Unknown effect, incomplete original callback, a foreign scope or a changed receipt must not advance the original intent. Lost recovery-CAS acknowledgement is resolved from the persisted revision. No schema or dependency version changes were needed for this batch.

## Verified locally

- Original failure: SQLite store reopen and fresh volatile/undeclared queues, both scheduler APIs, original Job ID and Fingerprint rediscovered.
- Seven native Root recovery variants: committed, strict rejection, allow-completion, foreign ledger, changed receipt, opaque callback and lost recovery-CAS acknowledgement. Run on Memory, SQLite, PostgreSQL adapter (SQLite-backed GORM), disposable real PostgreSQL 16.16, and independent encrypted Kernel/Harness SQLite databases reopened together. Original Tool executed once; correction and budget usage checked.
- Accounting acknowledgement negative tests: failure of Ledger settlement or projection must not release a qualified rejection.
- Agent, Kernel, Continuation and Harness package tests passed; repository `fmt-check`, `tidy-check` and diff whitespace check passed. Missing checksums for already-selected crypto dependencies were added to Harness PostgreSQL and detached Continuation integration module.

## Remaining admission gates and deliberate boundaries

- Full package sweep hit a local tool timeout; only finished suites are passing evidence. Full required CI, race and independent final audit of the eventual commit remain mandatory.
- A durable queue's AOF process crash with the SQLite pager and persisted Host cursor, durable queue namespace/restore loss or replacement, and the Gaoge downstream worker integration are not established by these tests. A preexisting watermark cannot prove survival of a different queue namespace. Do not mix previously acknowledged durable watermarks with a replacement volatile queue as though they were fresh delivery facts.
- New sealed settled Tool recovery covers native owned Roots with acknowledged original callbacks. Kernel child execution that lacks this whole-callback proof, a process killed before return sealing, or an opaque/incomplete Host callback still fails closed; a settled ledger alone cannot manufacture completion. Live catalog replacement and complete Harness worker restart are not covered by the new matrix. External asynchronous provider verification remains on the existing held-reservation contract. Approval admission shares the new scoped capture but its dedicated fault matrix has not been run.
- No business database migration or rollout performed. This is local implementation evidence, not merge approval.
