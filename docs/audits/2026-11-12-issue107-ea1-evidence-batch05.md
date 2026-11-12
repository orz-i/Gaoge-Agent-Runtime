# EA-1 B05 — fixed Wait and Resume acceptance

Date 2026-11-12. Frozen tested source: `d170665f48f3f0bb70df338b6ebced436042ad71`.

Eight fixed cells PASS: WR-R/WC-R P2; WR-W/WC-W P1/P3/P4. These exercise actual public Resume/ResolveWait entries. No substitute StartRun evidence.

Resume P2 sends a second original Effect after a yielded first segment, then receives unknown/pending without a receipt. Original intent is durable before dispatch. Fresh owners preserve exact Run/Actor/Thread/Definition/Effect/attempt/class/compensation/input and original Grant; repeated recovery/control cannot replay or fabricate settlement.

Wait P1 holds a successfully admitted Grant before checkpoint CAS or physical dispatch. P3 holds after original Wait response and exact full-EffectRequest-hash receipt persisted, before Mark. Both prove original owner exclusivity and independent peer rejection without partial mutation. P4 commits Mark, loses ACK, then recovers from a fresh owner without physical replay. Native SQLite really closes/reopens for P2/P4; PG uses independent connections; Memory/GORM are fresh owners, not claimed durable restart.

## FAIL → bounded fix → fresh-SHA evidence

Initial 793fbc2 exposed an actual EA-03 defect: descendant Grants blocked terminal ancestor writes but allowed valid nonterminal ancestor Apply. A separate fixture also used an invalid WaitingInput-to-WaitingInput transition, corrected to valid Running. Terminal-only variant 0c6fe33 passed but was explicitly NOT treated as full-cell acceptance. Initial failure and diagnostic variant logs remain preserved in their separate evidence directories and diagnostic report.

Independent limited review confirmed EA-03 requires all competing ancestor writes to be fenced. Four existing branches were minimally tightened: Memory Apply and ApplyControl (before execution epoch revocation), PostgreSQL and SQLite shared mutation transactions. Self's exact scoped permit and existing malformed-grant checks remain. No child permit is transferable to an ancestor, no global exception was added. Tests restore valid nonterminal ancestor negative vectors while retaining terminal vectors, including unsettled Wait P4.

At d170665 all four directed backend commands pass with -race; real PG runs every suite three times with no skips. Three module lint runs return 0 issues. All four full module race suites (core, PG with real ephemeral database, SQLite, Harness) PASS at the same SHA, including existing nested/subworkflow execution regressions. No new production path was bypassed to make tests pass.

Independent source review at d170665 found no remaining blocking findings within these eight cells. Final durable evidence has 11 logs, commands, origin and checksums under `docs/audits/evidence/issue107-ea1-b05/`.

Ledger now 44 PASS / 100 UNRUN. Older 36 PASS retain their actual earlier SHA/evidence; no claim of final unified-SHA acceptance. WP-RUNTIME and WP-GAOGE remain NO-GO; remote PR-head CI remains unexecuted under no-push/no-PR authorization. No Host changes, remote commits, PRs, merges or production data operations.

Mac disk 9.7 GiB before, 9.5 GiB after (minimum 9.4); cleanup 0 bytes, no active builds interrupted. Next fixed B06: Root/Child Compensation P0/P1/P2/P5 only, excluding already-PASS P3/P4.
