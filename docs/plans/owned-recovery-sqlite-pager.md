# Owned recovery专项：bounded SQLite child-terminal reconciliation

Baseline: `9c33f659f0ec0f7ad71ba236e60a310f9e0da0fd` (beta.24 candidate).
Downstream: Gaoge #97, fixed cohort remains unchanged until upstream review
and acceptance. This change does not retire any physical callback or authorize
replaying a model/tool or creating a child. The original Kernel grant, Agent,
Harness and budget receipts remain the sole effect authority.

## Scope and acceptance

1. SQLite RunRelationStore implements the existing optional
   ReconciliationPager with primary-key keyset pages bounded at the existing
   MaxReconcilePageSize. Cursor advances over scanned nonmatches; candidates
   require terminal child, running parent, changed native revisions. Relation
   identity stays immutable. Enqueue/observed watermarks are durable monotonic
   bookkeeping, not callback completion. Missing Runs are legal for relations.
2. Add explicit schema v9 with two nonnegative revision watermarks; exact
   v8 -> v9 migration is operator-selected, atomic, validates original schema
   and authority, preserves all existing rows; encrypted opening uses the
   original OpenConnection. Open never migrates. Historical migration steps
   continue validating their exact destination, not the latest schema.
   No existing Gaoge profile is migrated in this PR.
3. Short-lived hosts need an explicit cursor return API. Scheduler.ReconcilePage
   processes one bounded page from the caller's original cursor; returns the
   next scan cursor (empty on wrap); fails closed on a legacy adapter, never
   ListAll. Candidate errors remain reportable and unmarked, advancing across
   them so a poisoned row does not starve later pages; the next scan cycle
   retries original facts. On cancellation return the last processed position.
   Existing long-lived Reconcile retains its bounded retry queue semantics.
4. Exercise real SQLite including encrypted reopen, empty pages, nonmatching
   rows, monotonic/stale mark, failed enqueue and watermark, process replacement,
   tenant fence and exact queue identity. Do not invent a second scheduler
   or a direct downstream query of SDK-owned schema.

Separate recovery findings (exact rejected Tool return / callback completion,
over-budget delegation response qualification) require composition-level
proof and independent fixes. Neither is closed by this adapter.
