# Issue #107 EA-1 — WP-RUNTIME Evidence Batch 04

Date: 2026-11-12. Local-only acceptance progress; no push, PR, merge, production or Host changes.

Frozen tested source: `880253aad587914cc28d4b9177f40aa83d730142`.
Fixed cells: EA1-WR-R-P0, EA1-WC-R-P0, EA1-WR-R-P4, EA1-WC-R-P4. All four PASS.
These are actual public Resume entries, not substituted StartRun paths.

P0 rejects invalid revisions/missing Run and deterministically races a peer revision/terminal transaction before admission. Losing Resume sends no second Effect, strands no Grant, and preserves winning state. Child terminal rejection checks both child and ancestor unchanged, including repeated denial. Legitimate revision resumes exactly once.

P4 starts with one Effect then a yielded segment. Actual Resume commits its second original Effect receipt and settlement Mark, then loses ACK. Full EffectRequest hash binds receipt to Run, Actor, Thread, Definition ID/hash, Effect, attempt, kind/class, compensation flag and input. Forged Grant revision fails; fresh-owner first-party reconciliation retires only the original marked Grant without replay. Repeated reconciliation is exact ErrNotFound after successful retirement.

Four backend directed tests passed with -race. PostgreSQL16 runs count=3 with no skips and independent connections. Native SQLite durable close/reopen is specifically P4; P0 is a deterministic admission interleave. Memory/GORM exercise fresh owners, not durable restart. Three module lint runs report 0 issues.

Independent limited review found two fixture assertion gaps, not production defects: full-request receipt binding and rejected child snapshot checks. Both fixed at the frozen SHA; independent reviewer verified final delta and all seven evidence checksums/log outcomes, with no remaining finding in these four cells.

Evidence: `docs/audits/evidence/issue107-ea1-b04/`.
The ledger is 36 PASS / 108 UNRUN; older 32 PASS preserve their original source SHAs and evidence. Full final-SHA mandatory suites and unified ledger remain pending source convergence. Remote PR-head CI remains unexecuted under the user's no-push/no-PR instruction. WP-GAOGE remains independently blocked. P0 has no caller-Actor/route parameters and does not certify Host authorization.

Free disk 9.7 GiB before and after, reclaimed 0 bytes. No cleanup needed; no active builds interrupted. Next fixed B05: WR-R/WC-R P2 and WR-W/WC-W P1/P3/P4 only.
