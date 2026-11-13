# EA-1 batch 20: actual Harness AgentTask chain

Source: `8091d468a7ee721c51e055f5450f5819ca9dd114`.
Cells: H-AC P0–P5 only. Ledger: **132 PASS / 12 UNRUN**. The other 138 rows retain their original values and SHAs. Overall strict result remains **NO-GO**: MW-NEXT six cells, H-FROZEN six independent Gaoge cells, final common-source verification and release gates remain outstanding.

## Actual entry and backend scope

The tests execute real Harness StartWorkflowTurn → Workflow AgentTask → original Agent child → ResolveSubtaskApproval/CancelSubtask. This is a registry Tool, not Hosted MCP transport. The same entry runs on Kernel Memory, native SQLite and native PostgreSQL. P0–P2 use Host Memory; P3–P5 additionally use real Host PostgreSQL. Do not claim Host PostgreSQL covers the first three cells.

PostgreSQL uses independent pools. SQLite closes original and peer handles before reopening. P4 reopens both persistent Kernel and Host stores where configured and rebuilds Runtime/Runner routing. Memory retains its state. Immutable definition registries and the external relation recorder are retained; this is not an OS process-kill simulation. No adjacent Core test substitutes for this Harness chain.

P0 has ten concrete source/caller/checkpoint/runner/recorder negative vectors per Kernel backend, with full before/after Kernel and Host state, timeline/election, grant and shared-budget comparison. P1 uses actual Host and handler cutpoints in default and explicit-native configurations. Peer Kernel operations are competitors against real Harness-held authority, not the entry under certification. Full original Model request/catalog/provider/binding hash and actual Response, checkpoint and Tool request/result are checked; consumed-field pruning does not erase original captured source evidence.

## Cancellation authority configurations

An arbitrary Agent RunMiddleware makes that Agent cease declaring Kernel-owned cancellation. Existing Workflow control proof allows original Kernel-owned execution, trustworthy terminal observation, or the frozen Harness route with a quiescent pending approval. It does not promise arbitrary middleware can be cancelled while Running/unknown.

G11 and the P2 positive cases therefore explicitly use the existing Kernel-owned configuration without arbitrary Agent RunMiddleware, fixed from Create through reopen. The full Harness dispatch, approval, cancellation election, budget and receipt path still runs. A zero Agent Host-middleware counter does not mean Harness was bypassed. Both construction and reopen verify the actual OwnsKernelRuntime contract; no new self-proof capability exists.

P1 retains actual middleware. An opaque-unknown-tool P2 negative retains its middleware through reopen and proves exact RuntimeCancellationUnverified on initial, repeated and fresh-owner cancellation, before any election or budget mutation. Full Kernel/Host state/items/ledger/grants remain unchanged, Control0 and no new Model/Tool dispatch. The two positive P2 cases separately distinguish a nonempty returned child (Host returned fact, original Kernel unknown fence) from a receipt Apply fault returning empty state (Host scope also retained). Legal original CancelRun/election and Host conflict are not conflated.

## Receipt, recovery and ordering

P3 stops after the complete actual Tool result/receipt is durable but before the original child Mark. A verified terminal read-only Harness confirmation is distinguished from physical retirement: Control0, original child grant remains, Reconcile cannot fabricate Mark.

P4/G11 actually sends a busy Tool, creates the original cancellation election and authorized CancelRun budget transition, receives the same Tool receipt and original settlement, resumes the same uncompleted cancellation intent, commits Host completion but loses its ACK, then reopens stores. Original intent/receipt, full ledger and no duplicate physical send are checked. No budget reset or replacement intent is used to bypass unknown effects.

P5 has deterministic cancel-first and approval/Tool-first orderings, represented by the election and G11 suites respectively. The pre-Append barrier checks exact completion identity and legitimate admission with Seq=0. Actual persisted receipt verification occurs after Store sequence assignment; it never fabricates a future durable sequence. This Workflow has no compensation definition, so no compensation coverage is claimed.

## Same-source results and remaining gate failure

`evidence/issue107-ea1-b20/` contains six exact-source logs and checksums:
- Memory smoke: exit0.
- All-Store smoke: exit0.
- Host PostgreSQL module lint: exit0, zero issues.
- All four new top-level suites across their selected Store combinations, race, count3: exit0, no SKIP; actual PostgreSQL subcases ran three times.
- Seven existing Harness regression tests: exit0, including forty original approval/cancel race iterations.
- Full repository tidy-check: **exit1**, after passing this batch's Host PostgreSQL module and later reaching an independent `go/agent-runtime-redis/go.mod` normalization mismatch. The script restored original files. This real global gate failure is retained, not relabeled PASS. It remains for final local gates; the six matrix rows cite only the passing actual state-machine evidence.

No production Go behavior changed. The test module adds the two existing Kernel adapters, with precise local cohort replacements in go.work and the original tidy script's temporary-replace/restore mechanism. Final tidy normalized the new module through actual dependency resolution, not guessed hashes. Full-module race remains historical B14 source, not this source; final same-SHA complete gates remain required.

Three diagnostic directories remain intact: `issue107-ea1-b20-initial` (unpublished dependency graph before compilation), `issue107-ea1-b20-proof-smoke` (the correct opaque-configuration Unverified rejection, not a production invariant violation), and `issue107-ea1-b20-tidy` (five green test/lint logs plus the initial new-module tidy failure). Independent bounded source review accepted the original closed fixes, authority configuration split and final dependency-only delta; durable evidence review follows separately.

Disk: about 8.9 GiB initially, 9.2 GiB at final verification. No caches or evidence were deleted. No push, PR, merge, production data action or WP-GAOGE edit occurred. Next fixed batch is MW-NEXT P0–P5.
