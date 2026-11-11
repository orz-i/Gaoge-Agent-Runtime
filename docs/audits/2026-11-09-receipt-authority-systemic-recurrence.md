# 2026-11-09 — Repeated proof-authority class: systemic audit gate

**Status: BLOCKED for merge/delivery.** This is a root-cause and exit-gate record, not a completed governance round. Base: `main@b7a43fc28b2d4f2df7026a13eaffe42ac5d298b1`. Worktree: `governance/cancel-intent-receipt-authority-20261109`.

## Why this is an architectural recurrence

The November 4 Harness sealed-authority audit explicitly required **stopping piecemeal patches and reassessing Store trust/contracts if the vulnerability class recurred**. The November 9 Cancellation Fence defect meets that criterion.

The physical Execution Dispatch proof is already centralized in `VerifiedExecutionDispatchReceipt`; nevertheless a **separate Cancellation Fence** was treated as settled by a loose `ItemCompleted + ParentItemID` check in `ActiveCancellationFence`, `resumeCommittedCancellationFence`, and Harness PostgreSQL `pendingCancellationFenceTx`. Its Memory and SQL `AppendItem` paths did not require a canonical completion receipt. They thus admitted false facts that could authorize late Invocation/Interaction/physical execution once the cancellation gate appeared closed. This is an integrity defect within the documented trusted Store/Host write boundary; no external attacker exploitability has been established.

Targeted RED tests on the untouched baseline demonstrated false settlement for foreign Turn/Run, invalid boundary, early or duplicate receipts. RED also demonstrated that Memory could persist some forged completions. Separate SQL query/read consumers shared the weaker `exists completed` assumption.

Earlier PRs each proved their own **scope**, not the general proposition that every persisted authorization and completion fact across all feature paths was checked by a single authoritative type-specific contract. Full CI `make check` cannot cover a missing adversarial receipt class. Claims of whole-class closure were therefore unsupported.

## Proof-family inventory: distinct semantics, common integrity invariants

| Type | First-party verification/consumer | Further audit obligation |
| --- | --- | --- |
| Physical Dispatch Grant | `VerifiedExecutionDispatchReceipt`; Memory, SQL, Retry, cancellation | Exact grant/receipt + duplicate/contradictory facts; SQL insert/read/recovery parity |
| Cancellation Fence | Candidate `VerifiedCancellationFenceReceipt`; `ActiveCancellationFence`, SQL `pendingCancellationFenceTx`, `resumeCommittedCancellationFence` | Full cross-store RED/GREEN, corrupt historical rows, lost ACK, negative multi-fact family |
| Host business Interaction | `hasVerifiedInteractionResponse`; `unverifiedResolvedInteraction` and write-capable cancellation | Original Turn/Invocation/Run/Interaction, source parent, business receipt; concurrent Cancel/Resume |
| Runtime continuation claim/completion | `interactionContinuationClaimed`, `pendingInteractionContinuation`, `hasCompletedInteractionContinuation` | Audit claim identity, completion payload, original Owner/Run binding, contradictory facts, no duplicate physical Resume |
| Kernel identified Apply/Tool/Model | Separate native proof and epoch/receipt protocols | Original operation identity, anti-replay, persisted bytes and physical/no-effect boundaries; not interchangeable with Host Item facts |

**Do not introduce a universal FeatureDriver or a second state machine.** Prefer one small, pure, domain-specific canonical validator for each durable authority fact, invoked by every read/recovery consumer and by the actual Store `AppendItem` transaction. The list is an **audit hypothesis** for the remaining families, not an assertion that all of them have an exploitable defect.

## Required exhaustive invariants

1. A successful settlement binds **the original grant** and its exact immutable operation ID, Turn, Run, Invocation where applicable, stage/boundary and content identity. Enforce status/kind, `Seq > grant.Seq`, exact parent, and one unambiguous receipt. An empty/foreign/extra/contradictory receipt must not authorize future effects.
2. Write-side `AppendItem` must reject invalid or duplicate settlement **before COMMIT**, under the original Turn's serializing lock; read/restart paths must fail closed on already persisted malformed or legacy rows. Retry of exactly the same immutable ID is idempotent, but no new settlement ID may be minted for an existing grant.
3. Verification is the same across Memory and SQL transaction adapters (including real PostgreSQL cross-connection and SQLite fixture). Cross-Turn forgery must not escape owner-lock validation.
4. Failure/recovery matrix includes create/close/replay, CAS and lost ACK, context cancellation, partial Commit, race with new Invocation and physical Dispatch, foreign/same-ID owner collision, ambiguous external side effect, malformed JSON, multiple/late Item pages, and absent vs forged evidence.
5. Once an effect is possible but not conclusively settled, represent **UNKNOWN** and keep the original fence. Neither elapsed time, later unrelated revision nor a synthetic terminal state proves no effect.
6. Preserve valid behavior for original first-party emitters. Update **test fixtures** that minted noncanonical synthetic proof facts, not the security validator. If compatibility with old persisted facts is required, this is an explicit breaking-change/repair/quarantine decision, never a silent bridge.

## Hard exit criteria

**No PR, merge approval or 'governance closed' claim** until:

- A complete set of production consumer sites for each relevant proof family has been enumerated and reviewed, with exactly one canonical type-specific validator per family.
- Invariants × entry/recovery/write paths × invalid and concurrent inputs have permanent red/green regression tests on Memory/SQLite and real PostgreSQL. State-machine/property/mutation testing demonstrates that removing any critical identity/proof check is detected.
- All original positive authorization, cancellation, restart, no-effect and recovery cases remain green under repeated Race; full repository `make check` and necessary CI are green on the locked candidate.
- Independent review of the **entire affected proof chain** (not just patches), on final commit/PR Head, finds no unresolved P0/P1/P2 or unacknowledged cross-Store gap. Unrun paid live model calls, external service tests, third-party Store contracts and unknown risks are explicitly disclosed.

## Implementation and proof inventory (local, uncommitted)

The bounded implementation now addresses the original Cancellation Fence
hole and the two further same-class variants independently reproduced during
the proof-family inventory. These changes deliberately **do not** create a
universal Store state machine or an external Host router.

| Proof family | Authoritative producer | Pure verifier / read-and-recovery consumers | Memory + SQL pre-COMMIT checks | Adversarial evidence |
| --- | --- | --- | --- | --- |
| Physical Dispatch | `beginPhysicalInvocationDispatch`, `finishPhysicalDispatchGrant` | `VerifiedExecutionDispatchReceipt`, `PendingExecutionDispatch`, `pendingExecutionDispatchTx`, Retry, cancellation | `MemoryStore.AppendItem`, `checkExecutionDispatchCompletionTx` | Existing owner-bound and no-effect tests; new non-Diagnostic contradictory-child RED/GREEN in Harness and SQLite/SQL recovery |
| Cancellation Fence | `beginCancellationFence`, `finishCancellationFence` | `VerifiedCancellationFenceReceipt`, `ActiveCancellationFence`, `resumeCommittedCancellationFence`, `pendingCancellationFenceTx` | `MemoryStore.AppendItem`, `checkCancellationFenceCompletionTx` under Turn lock | RED foreign Turn/Run/boundary/duplicates; SQL invalid-insert rejection; direct corruption of persisted receipt remains fenced; cross-connection proof |
| Host callback result | `recordInteractionItem`, `recordVerifiedInteractionResponse` | `verifiedHostInteractionResponse`, `hasVerifiedInteractionResponse`, `unverifiedResolvedInteraction`, cancellation/Refresh/Resume | `VerifiedInteractionAuthorityAppend` shared by Memory and SQL | RED missing/wrong original Resolved Item, forged callback response, wrong ordering and owner; SQLite/real PostgreSQL dual connection |
| Runtime continuation claim / completion | `claimInteractionContinuation`, `recordCompletedInteractionContinuation` | `originalInteractionContinuationClaim`, `hasCompletedInteractionContinuation`, `interactionContinuationClaimed`, `pendingInteractionContinuation` | The same `VerifiedInteractionAuthorityAppend` checks the callback receipt, claim and completion chain | RED no original callback/claim, wrong Run, malformed completion payload, early and contradictory facts; no double Resume on lost ACK |

The original source-of-truth boundary remains the **trusted Store's atomic
Turn lock, immutable Items and exact CAS**. A privileged writer who can
fabricate mutually consistent records or delete original grants is outside
this security contract; no local SHA-256 checksum can authenticate a
fully compromised Store. The canonical validators nevertheless refuse
*self-inconsistent* returned rows, cross-owner substitutions, wrong source
records and ambiguous completions after a crash.

The new interaction proof reader is deliberately read-only. SQL
`checkInteractionAuthorityAppendTx` originally materialized the *entire*
Turn Item history under the Turn transaction lock. An independent design
review identified this unbounded-memory/performance regression before
delivery. The corrected SQL adapter traverses **at most three exact indexed
Item ancestors** (Resolved Interaction, Host receipt, Continuation claim)
and reads **at most two siblings** under each relevant authority parent
to detect ambiguous or contradictory receipts. A long-history (>1,300
unrelated Items) positive/idempotency regression protects that bounded
materialization. The sibling predicates still scan within the owning
Turn's indexed rows; these cold-path SQL lookups are **not proven
constant-time** and no throughput improvement is claimed. No new
index, schema, separate proof ledger or migration was introduced.

**Completed local evidence before independent final review:**

- Untouched-base RED tests: Cancellation Fence false release for wrong
  Turn/Run/boundary/duplicate receipts, Memory forged completion append;
  Continuation false completion without original business proof or claim,
  mismatched status/payload/Run and early sequence; Host verified receipt
  without original Resolved Item; Dispatch ignored a non-Diagnostic sibling
  under the same grant in both read/SQL write. Each received a targeted
  permanent GREEN regression.
- Existing full Harness and Harness SQL/SQLite Go module tests passed on
  the candidate after correcting old fixture-only noncanonical cancellation
  facts. Permanent SQL tests cover rejected malformed facts and verified
  recovery after a DB row's content is deliberately corrupted.
- **Historical v2 cohort local `make check`: PASS, exit 0.** Receipt:
  `coverage/systemic-proof-final/candidate-v2-check.exit`, covering Go
  fmt/tidy/vet/unit/race/lint, all modules, TS lint/typecheck/46 tests/build,
  Agent baseline, deterministic evaluation/benchmarks, local OTel/A2A and
  beta.24 clean-consumer release acceptance.
- **Historical v2 cohort real PostgreSQL/Redis integration: PASS, exit 0.**
  Receipt: `coverage/systemic-proof-final/candidate-v2-integration.exit`.
  Real Core PostgreSQL, Harness PostgreSQL, Redis and cross-process
  Continuation E2E suites passed, including newly added two-connection
  Cancellation/Host/Continuation proof tests and historical-row corruption
  refusal. The isolated `gaoge-agent-runtime-beta` Docker project was
  removed without touching unrelated services.
- **Historical v2 cohort bounded Go fuzz PASS** on Cancellation (**25,885
  cases**), Dispatch (**25,415 cases**) and Continuation (**25,112 cases**),
  with no counterexample over these short runs. Go source-corruption
  mutation overlays proved **four read-side critical clauses** and **five
  Memory/SQL write-admission guards** cannot be removed without making
  independent RED regressions fail. Original candidate remains GREEN.
- **Repeated Race PASS:** five repetitions of critical Harness cancellation,
  Dispatch, Host response, Continuation and lost-ACK scenarios; two
  repetitions of SQLite adversarial Store write, corruption and long-history
  regressions. SQL's original linked fact remains usable after **1,300
  unrelated Items** without materializing the full Turn history.
- **Baseline quality unchanged:** Python Agent protocol **131/131 PASS**;
  fixed scripted Agent quality **40/40 PASS** with 160 simulated Model calls,
  120 simulated Tool calls and original dataset hash
  `sha256:9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`.
  Both historical paid-model reports were verified read-only against their
  original public SHA-256 anchors. No new paid Model/gateway call or
  production speedup is claimed.
- Changes remain **uncommitted**, with no push, PR or merge. Main and
  both user-owned untracked OpenAI probe scripts remain unchanged.

## Cross-path root-cause remediation extension (resumed governance)

The independent re-audit discovered failures *after* the prior candidate
passed full CI-equivalent local tests. The recurring defect class was
**using generic status / ParentItemID filters as evidence of no outstanding
physical authority**. The revised design enumerates all reserved original
facts first, and only the type-specific verified contract may settle them.
It remains a hard-cut evolution of the existing Item ledger and Turn CAS,
not another persisted state machine.

| Invariant / proof family | Shared original authority contract | Read, write, fault-path coverage |
| --- | --- | --- |
| One business response writer | `Store.UpdateInteraction` removed from interface, Memory, SQL and Go consumer; `ResolveInteraction` alone owns the Turn/Invocation CAS | Root and child callback ACK-loss regression, changed persisted response after callback, original Waiting/Resolved Item payload identity |
| Valid Host source → callback → continuation | `verifiedInteractionSource` verifies both original Item statuses and complete business payload; `classifyContinuationEvidence` derives absent, returned-but-unclaimed, claimed, completed or contradictory from immutable facts | Root Agent and child Team cannot physically Resume after a committed business response with lost ACK but no continuation Claim |
| Unambiguous first-party authority JSON | `authorityStringFields` and `sameUniqueAuthorityJSON` reject duplicate keys (including nested/escaped), unknown fields and mistyped boundaries | Permanent Memory/SQLite response, claim, cancellation and dispatch RED/GREEN cases |
| One reserved original and completion identity | `ValidReservedAuthorityOrigin`, `ValidReservedAuthorityCompletion`, and each type's exact verifier are invoked by both Store appenders | Rejected malformed grant origin, preoccupied original Host Item ID, orphan settlement, wrong phase/Run/Turn, duplicate or foreign sibling |
| Corrupt original never disappears on read | All Memory, SQL, Retry and native Epoch admission checks enumerate reserved parent identity **without filtering by expected Kind/Status first** | Direct mutation of persisted original Cancellation and Dispatch metadata remains fenced on both Stores; repeated recovery and Retry assertions |
| Bounded SQL proof chain | At most three indexed ancestor reads and two children per authority parent | Full original callback/claim/finish can follow 1,300 unrelated Items without scanning or materializing complete Turn history |

The original Kernel/Provider Model, Tool, Apply and Epoch protocols stay
separate, and no custom Host Store is permitted to treat unknown physical
effects as proven no-effect. The `UpdateInteraction` removal is a deliberate
**breaking pre-v1 API correction**, without compatibility adapters.
Previously persisted noncanonical proof rows must be quarantined or repaired
explicitly, never silently adopted as execution authority.

Additional independent Go Overlay mutation tests confirmed that relaxing
the callback-returned/claim distinction, ignoring original business payload,
accepting orphan completion IDs or filtering corrupted SQL grant metadata
turns permanent regressions RED. The earlier nine Store/read mutation gates
and bounded three-family fuzz tests remain baseline evidence, not proof of
all possible future failures.

**Current v4 source cohort** remains uncommitted on the existing governance branch.
Its full `make check` ran on the current frozen Go-source fingerprint
`de4ca70b04290fe2dc31d6ea886358ed3f45261c81dcd94fce1f1797f5256e15`
and **passed, exit 0**, including Go full Race/Lint, TypeScript 46 tests,
OTel/A2A, portable benchmarks and beta.24 clean consumer.
Receipt: `coverage/systemic-proof-final/candidate-v4-check.exit`.
This is local acceptance, **not GitHub CI on a PR Head**.
The current v4 source cohort also passed the real PostgreSQL 16/Redis
integration and cross-process Continuation E2E with **exit 0**:
`coverage/systemic-proof-final/candidate-v4-integration.exit`.
This includes the real Harness SQL and cross-connection authority,
cancel/retry, loss-of-ACK and process crash/recovery scenarios. The
isolated Docker project was subsequently torn down. Three final-source
bounded fuzz runs independently passed 31,996 Cancellation cases,
33,361 Dispatch cases and 30,049 Continuation cases (95,406 total).
No paid live-provider quality or production performance improvement is
claimed. The remaining gate is a **separate independent final
whole-chain code review** followed by locked-commit PR CI.

**Still blocking delivery GO:** the separate independent code audit of the
complete affected execution/receipt/Store-recovery chain, segmentation of
reviewed source into final commits, and all required GitHub CI checks on the
actual locked PR Head. This local source cohort is tested but **uncommitted**;
there is no PR or merge authorization. The exit gate above remains active.
Unknown risks include untested arbitrary malicious Store writers (outside
the trusted Store contract), production-scale sibling query cost, missing
live paid-model/gateway 40-case quality evidence and deployed OTel Collector
behavior. No blanket exactly-once guarantee is asserted.


## Follow-up independent audit: terminal Store CAS admission

An independent read-only Go Overlay fault demonstrated P1 in the prior
candidate. After the Host response and unique Continuation Claim were
committed, the elected physical Agent.Resume was never entered (lost Claim
acknowledgment). If the independent Kernel Run became Completed, a public
Refresh could prematurely finalize the original Harness Turn.

A second SQL Store test demonstrated that direct UpdateInvocation(Completed)
and UpdateTurn(Completed) could bypass the same claim: the original Runtime
continuation had no durable Completed receipt. Runner-only observations
cannot be the sole authorization boundary because Claim append and terminal
CAS are concurrent physical operations.

The next local hard cut shares one read-only terminal projection predicate
between Memory and SQL. Both Stores now validate non-idempotent
Completed/Failed Invocation and Turn transitions UNDER their original
serialized Turn lock. SQL gathers only exact original resolved Interaction
and Host proof IDs and descendants. The one elected Host callback context
can project its own returned physical result before appending its separate
completion Item; unrelated callers cannot obtain this private capability.
Cancelled transitions retain their independent original cancellation
proof and Kernel no-effect policy.

New permanent Memory and SQLite tests reject both terminal CAS operations
before the original Continuation Completed receipt and accept them after
the exact completion. A real PostgreSQL two-connection test checks the
same inter-process serialization. The former SQL retry fixture no longer
terminalizes an accepted child while its Dispatch Grant lacks the original
completion. All full Harness and Harness SQL module tests, and repeated
targeted Race on those proof boundaries, passed on the local candidate.

Four independent source mutations that each disabled a Store terminal CAS
proof turned its regression RED; the original source was GREEN. Removing
only the Runner's early Refresh observation did not defeat the underlying
Store CAS, which is the authoritative physical fence. The public
observation remains for nonterminal projection paths and clearer errors.

The current candidate still requires a new full repository make check,
the actual real PostgreSQL/Redis integration, and independent review of
the complete final diff before submission or PR approval. The prior v4
receipts are historical comparison evidence only.

## Additional direct-Cancelled Store CAS audit

After the v5 terminal CAS work, a separate read-only SQL fault reproduced
another same-class status bypass: UpdateInvocation(Cancelled) succeeded while
the original physical Start grant was in doubt, without an admitted Harness
cancellation intent. Permanent Memory/SQLite RED-to-GREEN tests now require
the physical grant to be settled before such an unprotected cancellation.
The original native Kernel cancellation path remains authorized *only if*
a same-Turn cancellation intent has already been committed; it is not
silently treated as proof the physical Start itself returned or that
descendant cleanup was complete.

The Memory and SQL implementations share the physical grant/intent
predicates under their original Store mutex / Turn lock. A trusted custom
Store must provide the same atomic semantics. TurnCancelled remains governed
by its separate Kernel cancellation workflow and is not conflated with
Completed/Failed Host continuation projection.

One complete SQL module run after this change encountered a transient
conflict in the canonical continuation completion fixture (done-wrong-ID).
The isolated case and a subsequent identical full module run passed. This
is a *stability observation*, not proof that the flake class is absent;
repeated Race and real PostgreSQL integration on the final candidate
remain required before independent merge-admission review.

## Independent terminal Turn CAS versus original physical grant

Another read-only SQL Go Overlay audit established P1: a Turn with no
resolved Host Interaction could still commit Completed while an original
physical Start grant remained outstanding. The previous terminal CAS proof
only consulted Host business callback/continuation facts. Store UpdateTurn
is a separate authority boundary: an independent Kernel status snapshot
alone cannot certify a remote or locally routed physical Start returned.

Memory and SQL terminal Turn CAS now verify both the original Host
Continuation proof AND active Dispatch/Cancellation fences under the same
original Turn lock. Only the original elected Kernel cancellation path,
after its separate Kernel terminal and descendant cleanup confirmation,
can carry the private, Turn-bound verified-cancellation projection context.
A passive Refresh, generic Store client or stale Kernel snapshot cannot
mint it. This is not a new persisted state machine or a blanket
exactly-once claim.

Permanent positive/negative tests cover both Completed and Failed Turn
transitions across Memory/SQLite without an Interaction: outstanding
physical grants deny terminal CAS; the exact original no-effect completion
releases the gate. The original root native Cancel and descendant-cleanup
regressions still execute through their canonical fenced path.

The older SQL Retry fixture had previously forced root Failed before an
unknown child physical Start finished. That state cannot be generated by a
correct post-hard-cut Store. It now explicitly checks that root terminal
projection and parent Attempt rotation are rejected rather than creating
this unsafe state for the subsequent test. Legacy pre-hard-cut rows remain
subject to the independent restart/retry fences and require explicit
quarantine or repair.

Full v7 repository, real database and independent final review have NOT
yet been accepted. Earlier v5/v6 receipts refer to different source
cohorts and are not a merge gate.


## Final cross-product Turn terminal authority matrix

A further independent SQL Overlay test found that direct TurnCompleted
could terminalize a Turn with no Resolved Interaction while its original
physical Start Grant was outstanding. This revealed a broader design
mistake: Host callback proof alone was being reused as if it covered all
native/remote physical authorizations.

The corrected Store terminal CAS performs both original Host continuation
validation and original physical Dispatch/Cancellation validation inside
the same Turn-serializing lock, not merely an unlocked Runner preflight.
A Turn with zero Interactions still must account for all its original
physical grants. For Completed/Failed, all outstanding grants/fences must
be settled, except the elected original native Kernel cancellation callback
carrying its unforgeable, Turn-scoped verified-cancellation context after
descendant cleanup. A Cancelled transition similarly cannot pass with
an unresolved grant unless that exact original trusted cancellation path
has both the active durable intent and verified Kernel/descendant proof.

The three terminal target states are expressed by one pure
ValidTurnTerminalEffectProjection predicate, called by Memory and SQL.
The SQL implementation reads existing indexed original physical grant
and cancellation facts inside the same Turn row lock; it does not
invent or persist an independent state machine. This preserves the
separate original Kernel cancellation authority and never treats
a synthetic terminal status as proof of an unobserved effect.

Permanent table-driven Memory and SQL/SQLite tests cover Completed,
Failed and Cancelled with a pending physical grant, and their legitimate
release by the exact no-effect receipt. The original first-party native
Agent cancellation, retry and accepted child scenarios also remain green.
The original SQL Retry fixture was corrected to reject prematurely
terminalizing its root while a child grant was still unknown, rather
than staging an impossible post-hard-cut state.

Earlier v5/v6/v7/v8/v9 local check records are historical. The
v9 full `make check` passed (exit 0) after correcting an exhaustive
TurnStatus lint case, but its source was superseded by another
independent authorization audit; it is **not** the final delivery gate.

## Same-Invocation / different-Interaction witness isolation

A separate read-only Go Overlay audit found another original identity
gap: `completedInteractionCallback` carried only Turn and Invocation,
not the original Interaction ID. Two Resolved Interactions may belong
to the same Invocation across time. The elected callback for B could
then authorize a pending physical continuation for A through both the
`pendingInteractionContinuation` read predicate and Memory/SQL terminal
CAS proof, despite A having no completion receipt.

This is a *distinct persisted original-identity error*, not a generic
terminal status issue. The private callback witness now binds the
exact **Turn × Invocation × Interaction** tuple. All three consumers
(`prepareResolvedInteractionContinuation`,
`pendingInteractionContinuation`, and
`ValidTerminalProjectionProof`) use the same exact match. It adds
no new persisted execution class, compensating router or compatibility
bridge.

The permanent `TestOriginalInteractionCallbackWitnessCannotCertifySameInvocationSibling`
checks BOTH negative and positive paths: a callback for B never
certifies pending A, while a fully completed A does not block the
elected B from legitimate continuation. The original RED Go Race
Overlay proved the pre-fix Owner-only predicate was unsafe; the new
actual Runner proof and complete Harness module tests passed.

An independent four-variant overlay also proved that removing any
one of the **Memory Invocation, Memory Turn, SQL Invocation, or SQL
Turn** atomic terminal CAS checks causes the corresponding permanent
regression to fail. Removing only the early Runner read guard did
*not* defeat the final Store CAS and is correctly treated as
defense-in-depth rather than as an uncaught write-authority bypass.

## V10 local implementation acceptance — passed; independent merge audit outstanding

The **final uncommitted V10 Go source fingerprint** is
`1ec754bbec494b83de70c0688d717771f69fb6c86e7603a596b6cb280e632017`.

- **Full `make check` PASS, exit 0**, exact same source cohort:
  `coverage/systemic-proof-final/candidate-v10-check.{log,exit}`.
  Includes Go fmt/tidy/vet/unit/race/lint across every module, TypeScript
  lint/typecheck/46 tests/build, A2A/OTel local acceptance, portable
  benchmarks and beta.24 clean-consumer release checks.
- **Real PostgreSQL 16 + Redis integration PASS, exit 0**, exact cohort:
  `coverage/systemic-proof-final/candidate-v10-integration.{log,exit}`.
  Includes Core/Harness PostgreSQL, dual-connection original Host callback,
  cancellation receipt and terminal CAS vs continuation Claim, Redis
  worker/lease recovery and cross-process Continuation E2E. The
  disposable Docker project was removed after completion.
- **Scripted Agent baseline 131/131 PASS**, fixed offline quality
  **40/40 PASS**, 160 synthetic Model calls/120 synthetic Tool calls
  and unchanged dataset SHA-256
  `9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`.
  Evidence:
  `coverage/systemic-proof-final/candidate-v10-baseline.log`,
  `candidate-v10-quality.json` and both unchanged historical paid-model
  reports verified read-only against their original public anchors.
  No new paid Provider/gateway run or live-quality improvement is claimed.
- **Bounded V10 Go Fuzz PASS:** Cancellation 20,190, Dispatch 19,775
  and Continuation 17,774 executions (**57,739 total**). Logs:
  `coverage/systemic-proof-final/candidate-v10-fuzz-*.log`.
  The original three-family fuzz seeds remain permanent tests.
- **Repeated Race PASS:** root/child ACK loss, terminal CAS, same-Invocation
  sibling Interaction (5 repetitions) and SQL terminal/cancellation/
  dispatch/long-history original-proof validation (2 repetitions).
  Five independent source-clause mutations on the original CAS and
  callback-ID invariants all turned the permanent tests RED, while
  the candidate remained GREEN.
- Code remains **uncommitted and unpushed**, with no PR or merge. The
  main worktree remains at the prior verified `main` SHA and the two
  user-owned OpenAI probe scripts are kept intact. An identical Go-source
  fingerprint was verified after the final tests.

**Remaining hard gate:** a separate independent final review of *the entire
terminal status / physical dispatch / Host callback / continuation / Store
recovery chain* on the frozen candidate. After that, use scoped segmented
commits, push a PR and verify every required GitHub check on its actual Head.
The local V10 results alone do **not** constitute final merge admission.
Known limitations: an arbitrary malicious privileged Store writer remains
outside the trusted Store contract, no live full paid 40-case benchmark,
no deployed OTel Collector E2E and no measured production performance gain.

## November 10 — post-v10 independent NO-GO terminal protocol hard cut

Independent review of v10 identified five P1 families across Memory/SQL:
a pending cancellation intent falsely authorized a terminal cancellation;
an unrelated sibling's cancellation authorized another Invocation; a
waiting Host Interaction was ignored; a physical Start return was mistaken
for a terminal Kernel Run; and an immutable Turn terminal outcome could
be rewritten. The original independent report is stored outside this
worktree at /tmp/gaoge-v10-independent-final-audit-20261110.md.

The current candidate consolidates all terminal Turn/Invocation transitions
in terminal_transition_authority.go, with the same original operation
witness or original no-effect grant/receipt chain checked under the Memory
mutex or SQL Turn row lock. Dispatch returned does not prove Runtime
completion; no-effect allows Failed but never Completed/Cancelled; a
terminal Turn outcome remains immutable absent an explicit safe Retry
Attempt rotation.

A terminal Turn also requires its original child Invocations to be
terminal. Handoff children have a separate physical execution owner and
can remain Harness InvocationAccepted even after verified remote success.
The shared VerifiedTerminalHandoffChild predicate accepts such a child
**only when** its frozen original input, Started Item, unique terminal
Item and handoff_start_returned grant/receipt all match the exact
Turn/Root/Child Run. SQL queries those original indexed facts under the
same terminal CAS lock. A statically composed local ChildRunner can be
read-only observed through the original Handoff Coordinator, and its
terminal result projected as an immutable Handoff Item before the root
terminal CAS. A routed/remote/unverified child cannot claim this path.

The original failure/cancellation/Retry tests were updated only where
they had minted terminal statuses without a physical or no-effect proof.
The explicit cross-Store positive test proves root+child no-effect receipts
allow legitimate terminal Failed and one Retry Attempt. Eleven additional
negative Handoff proof variants and positive asynchronous/local
Delegation recovery remain permanent tests.

Full Harness and Harness PostgreSQL module unit suites currently pass.
**Still BLOCKED for delivery** until full repository make check,
real PostgreSQL/Redis cross-process integration and a new independent
whole-chain code audit pass on this later source cohort. No commit,
push, PR or merge is authorized.

## November 10 — frozen terminal authority local integration candidate

The latest **uncommitted/unpushed** candidate Go-source aggregate SHA-256:
`ebedb9c78237a6c9a2450e73343d269c663db460457975498f34b1df75166d77`.

The five independent P1 counterexample classes are covered by permanent,
cross-Store terminal transition tests. This revision also closes a
legitimate Application cancellation/cross-ACK recovery regression and
preserves local asynchronous Delegation/Role/HostedTool execution.
A rejected physical Start has a narrow, original durable no-effect
receipt which may safely project **Failed**, never Completed or
Cancelled. Missing/ambiguous/foreign evidence cannot mark a Run terminal
or authorize an Attempt rotation. Child liveness is part of terminal
Turn authorization, with the separate original Handoff ChildRunner
terminal Item/dispatch-return proof required for local delegated children.

**Final local acceptance evidence on this exact Go source cohort:**

- `coverage/terminal-authority-final/check-v3.exit`: **0**;
  `check-v3.log`: full `make check` Go formatting, vet, module unit,
  full Race, Lint, TypeScript tests/build, benchmark smoke, A2A product
  and clean beta.24 consumer checks.
- `coverage/terminal-authority-final/integration-v4.exit`: **0**;
  `integration-v4.log`: real PostgreSQL, two independent connections,
  Redis and cross-process Continuation E2E. The positive parent+child
  no-effect → original Failed → TurnFailed → single legal Retry test,
  unconfirmed Continuation CAS denial, terminal immutability and unknown
  native/remote child Retry fencing all passed on real PostgreSQL.
  Dedicated Docker containers/network removed by the integration runner.
- `coverage/terminal-authority-final/baseline.log`: **131/131** Agent
  protocol tests. `quality.json`: fixed offline scripted **40/40**
  (160 synthetic model calls, 120 synthetic tool calls) with unchanged
  reference dataset SHA-256. Original historical paid-model report
  hashes verified read-only, with **no new paid live benchmark**.
- `coverage/terminal-authority-final/fuzz-*.log`: bounded native Go
  Fuzz for Cancellation (35,910 inputs), Dispatch (36,657) and
  Continuation (30,937), **103,504 combined**, no failing counterexample.
  This is bounded fuzz coverage, not mathematical proof.
- `harness-race-repeat.log`, `sql-race-repeat.log`: repeated
  `-race -count=3` on the terminal P1 invariants, Handoff proof
  corruption cases, root/child Host ACK loss, original callback owner,
  no-effect/Retry and SQL original proof Store. Both passed.

**Historical failed gates preserved:** earlier
`integration-v2` and `integration-v3` had exact, later-corrected
real PostgreSQL test fixtures inconsistent with the breaking terminal
authority contract (oversized generated Item ID and direct terminal
status without original Run proof). No production predicate was relaxed
to make these fixtures pass. `check` (before final enum Lint) and
`check-v2` are comparison only; `check-v3` and `integration-v4`
are the final source cohort acceptance evidence.

**Remaining hard BLOCKED gate:** independent *final* source-chain
review on the complete current candidate, especially original witness
minting, Handoff terminal provenance, SQL write/read Turn locking,
no-effect and cancellation/Retry cross-boundary behavior. The above
local tests are not independent merge admission; GitHub required checks
on the eventual exact PR Head have not run, and no PR, push or merge
has occurred.

## November 10 — P1/P2 independent audit remediation (repair-v1)

An independent final code audit **rejected** the preceding otherwise fully
tested local candidate: `/tmp/gaoge-terminal-authority-final-audit-20261110.md`.
The independent Memory, SQLite and real PostgreSQL two-connection negative
test reproduced a **P1**: a no-effect Control/Resume result for an already
Running original Kernel Run was promoted to `InvocationFailed`, then
`TurnFailed` and finally a second physical Retry Attempt. The same audit
identified a **P2** Handoff terminal-proof ambiguity: Go's permissive
JSON decoder silently accepted duplicate/last-key-wins status keys in
otherwise valid-looking terminal proof payloads.

This repair uses **one provenance separation**, not a terminal-status
exception: a settled Dispatch operation proves only *that operation*.
`VerifiedOriginalStartNoEffect` accepts exactly one immutable
`start` (Attempt 1) or `start-retry` (Attempt >1) grant whose frozen
Turn/Invocation/Run match the current attempt, and exactly one
original `physical_dispatch_rejected`, `feature_start_no_effect` or
`native_agent_start_no_effect` receipt. Any returned Start, duplicate
grant, Control/Approval/Resume no-effect, Handoff return or foreign
Attempt cannot authorize the original Run's terminal Failed projection.
Memory and SQL call this same validator inside the owning Turn CAS.

Retry also checks the original physical lineage under the Store's Turn
lock through `RetryOriginalOperationEvidenceValid`; SQL reuses the
bounded, shared `originalEffectFactsTx` loader for both terminal
projection and Retry. A historical persisted Failed/Cancelled record
whose only observed original-Run operation was a harmless Control/Resume
is **not allowed to rotate Attempt** merely because its status is
terminal. Legitimate original Start no-effect, root/child Failed and
explicit Retry positives remain accepted. The no-grant case still relies
on the trusted Store's original Kernel/Host terminal CAS and other
independent cancellation/child fences; a malicious privileged writer
that fabricates a coherent legacy terminal row with no physical facts
remains outside this contract.

Handoff now uses the same shared duplicate-free JSON scanner and a
strict typed round-trip proof for its original child input, Started and
terminal Items. Its typed frozen input explicitly models the legitimate
`harnessRoot` envelope (root Invocation/Run/Attempt), rather than
silently discarding it. Duplicate and escaped keys at arbitrary depth,
case-insensitive field aliases, unknown fields, ambiguous status/result
and mismatched root identities fail closed. Non-authority Artifact
siblings remain permissible, but cannot vote as a second terminal
Delegation result.

Permanent regression evidence includes
`terminal_no_effect_scope_test.go`,
`terminal_retry_historical_proof_test.go` in both Store modules,
`terminal_handoff_authority_json_test.go`, and a real PostgreSQL
two-connection `TestRealPostgresControlNoEffectCannotGrantSecondPhysicalAttempt`.
Original negative cases were observed RED before the implementation,
then GREEN after the shared predicate correction. Root/child no-effect
and sealed Handoff positives remained GREEN.

**Local real-service acceptance for frozen repair-v1 Go sources**:
SHA-256 `3cccbdba1102e7b41543bff474e740a985f2d8d05faf887a6d0ea4eded189a87`.
`coverage/terminal-authority-final/repair-v1-integration.exit=0`.
Real PostgreSQL, Redis, dual-connection terminal/Retry correction and
cross-process Continuation E2E all passed; disposable test containers
were removed. A full `make check` on this exact source is being rerun
at `coverage/terminal-authority-final/repair-v1-check.{log,exit}`.

**Still BLOCKED for formal delivery:** exact-source full check, independent
final whole-chain code audit (including the newly shared Retry recovery
boundary), and required GitHub CI on a future locked PR Head. No commits,
push, PR, merge or paid live-model verification have been performed.

## November 10 — repair-v2: Start effect, Kernel terminal witness and Retry scope

Independent audit report `/tmp/gaoge-terminal-authority-final-audit-20261110.md`
confirmed a P1 original-Run authorization failure: a harmless
`resolve-approval` or `resume` no-effect was accepted as proof the
original Kernel Run failed, and a historical Failed row could rotate a
second physical Attempt. It also confirmed P2 ambiguous Handoff terminal
JSON. The first local repair closed current terminal CAS but a later
independent Overlay exposed a related historical case: an **effectful
original Start return + harmless Control no-effect + corrupt legacy
Failed** could still rotate the Run. The same independent negative
case was then captured permanently in Memory and SQLite and added as a
real PostgreSQL 16, two-connection regression.

**Structural authority protocol after remediation:**

- `VerifiedOriginalStartNoEffect` is strictly the sole original
  `start` (Attempt 1) or `start-retry` (subsequent Attempt) physical
  grant plus its unique pre-effect rejection, bound to exactly the
  current Turn, Invocation and Run. Control, Approval, Resume, Handoff,
  duplicate grant or a returned effectful Start never certifies the
  original Run failed.
- `RetryOriginalOperationEvidenceValid` is independently evaluated
  at the Store's original Turn-serialized Retry CAS. An exact original
  Start with definitive no effect can recover without an actual Run.
  An **effectful original Start**, even if the persisted Invocation
  already says Failed/Cancelled, requires a private terminal Kernel
  witness. Only the original Runner can mint that witness after
  read-only loading and confirming the actual frozen physical root or
  child Run, its terminal status, and its Turn/Invocation/Run/Attempt
  identity. A live Running/Waiting/Paused Run or wrong Run/status
  cannot mint it. Both Memory and SQL consume the same proof.
- The SQL `originalEffectFactsTx` retrieves original physical grants
  and *all* their child records within the same Turn lock for both
  terminal Failed and Retry. Historical rows whose only proof was a
  harmless Control/Resume fail closed. Host-only Application/no-grant
  transitions continue to use their separate original trusted Host
  result/cancellation and child authority; the trusted Store cannot
  authenticate a privileged external writer who fabricates a fully
  coherent new ledger.
- `decodeTypedAuthorityJSON` reuses the existing duplicate-free
  recursive JSON authority parser and requires a canonical typed
  round-trip. The original Handoff child input explicitly models the
  legitimate sealed `harnessRoot` with root Invocation, Run and
  Attempt, while Started and terminal Item payloads reject unknown
  fields, casing aliases, duplicate JSON keys (including escaped and
  nested forms) or semantic changes. Non-authority Artifact children
  do not vote for a Handoff completion; competing terminal Delegation
  results remain invalid.

**Exact frozen local candidate source:**
`46144395f3da9e962312961c77f94d1672db31b796c043daf7ac2753c4453445`
(aggregate SHA-256 over sorted Harness and Harness PostgreSQL Go files).

- `coverage/terminal-authority-final/repair-v2-check.exit`: **0**,
  exact-source complete `make check`, all Go module unit/Race/Vet/Lint,
  TypeScript tests/build, A2A/OTel local boundaries, benchmark smoke,
  and beta.24 clean-consumer release gate.
- `coverage/terminal-authority-final/repair-v2-integration.exit`: **0**.
  Core PostgreSQL, Harness PostgreSQL two-connection P1 regressions
  (including effectful Start + harmless Control + historical Failed),
  Redis and cross-process Continuation E2E all PASS. The dedicated
  Docker test network and containers were removed.
- `coverage/terminal-authority-final/repair-v2-baseline.log`:
  Agent protocol **131/131 PASS**; `repair-v2-quality.json`: fixed
  offline 40/40 scripted calibration with 160 synthetic Model calls
  and 120 synthetic Tool calls, original dataset SHA-256 unchanged.
  Two historical paid-model evidence anchors passed read-only SHA
  verification. **No new paid Provider/gateway or deployed production
  benchmark was run.**
- Native bounded Go fuzz on this source: Cancellation **23,067**,
  Dispatch **22,918**, Continuation **18,616** cases (**64,601 total**),
  no counterexamples. Logs `repair-v2-fuzz-*.log`.
  Runner Retry paths were also tested twice under `-race`, including
  frozen RequestID, true terminal Retry, native cancellation and
  historic no-effect fail-closed paths. Both independent Go Overlay
  mutations (skip private original-Kernel Retry witness; allow duplicate
  Handoff JSON key) turned permanent regressions RED; unmodified
  candidate remained GREEN.

**Delivery gate remains BLOCKED:** an independent *post-fix* whole-chain
code review of this frozen source plus required checks on an eventual
locked PR Head. The local success above is not independent merge
admission. No commit, push, PR or merge is authorized. Other risks:
trusted-Store contract excludes arbitrary privileged out-of-band
coherent ledger writes, custom Store semantics not covered by
Memory/SQL conformance, deployed Collector behavior and unmeasured
production load.

## November 10 — resumed independent frozen-ancestor Handoff audit (repair-v3)

After repair-v2's locked source and full local acceptance passed,
a separate read-only Go Race audit examined the complete frozen Handoff
`harnessRoot.parents` ancestor Attempt list. It discovered that the
terminal verifier compared only the root Attempt and ignored malformed
or stale intermediate parent bindings. A child Handoff could be marked
terminal even when its frozen parent was on Attempt 1 but the persisted
intermediate Invocation had rotated to Attempt 2. This is an
execution-authority provenance gap independent of duplicate JSON keys.

The implementation now reuses the **single existing `frozenChildRoot`**
validator for original sealed input, including all ancestor Attempt
identities and duplicate/invalid parent checks. Unsealed pre-hard-cut
Handoff input is no longer a terminal authority. The shared
`ValidTurnTerminalDescendants` predicate additionally verifies each
intermediate parent Run/Attempt against the current Invocation rows
loaded under the same Memory mutex / SQL Turn-serializing transaction.
The positive fixture uses the actual first-party `sealChildRootInput`
emitter, not an invented unsealed test shortcut.

Permanent RED→GREEN regressions:
- `TestTerminalHandoffMalformedAncestorBindingsCannotSettleRoot`:
  duplicate ancestor, repeated root and invalid frozen Attempt;
- `TestTerminalHandoffFrozenIntermediateAttemptCannotCrossRetry`:
  valid original intermediate parent Attempt remains accepted; a
  rotated intermediate parent Attempt denies stale child terminal proof;
- `TestTerminalHandoffAuthorityRejectsAmbiguousOrChangedJSON`:
  self-consistent rehashed bad-input JSON reaches the strict semantic
  verifier (not merely an InputHash mismatch); missing `harnessRoot`
  is rejected;
- original root/child terminal positive and nested Role/Handoff/HostedTool
  product tests continue passing.

This changes no SQL schema or separate execution state machine. The
hard-cut behavior for previously persisted unsealed Handoff sources must
be treated explicitly as blocked/quarantined until they can be repaired,
not silently accepted. Local full repository and real PostgreSQL/Redis
acceptance must be rerun against the latest Go aggregate SHA; prior
repair-v2 local passes are historical reference only.

**Formal delivery still BLOCKED** pending the new exact-source local
checks and separate independent full-chain audit. No commit, push, PR
or merge has been authorized.

### Final ancestor-Attempt invariant across all Handoff child statuses

A second independent RED test proved the same stale ancestor Attempt
could be bypassed by changing the Handoff child's own Harness status to
`InvocationCompleted`: the original Turn validator previously skipped
all already-terminal children before checking their immutable parent
Attempt binding. The shared `ValidTurnTerminalDescendants` now compares
the complete frozen root/intermediate Run and Attempt chain for *every*
recognizable Handoff child, including previously terminal children, then
uses a separate original Handoff terminal/dispatch proof only when the
child is still nonterminal. A matching intermediate original Attempt
is accepted; the same child under a rotated intermediate Attempt is
rejected in both Accepted and Completed states. Permanent RED→GREEN
tests and repeated Go Race checks pass, as do the entire Harness and
SQL Harness module test suites. This is still one shared Store terminal
admission predicate, not a new Handoff-specific writer bypass.

The previous `repair-v3-check` started before this final production
change and is **historical only**, whatever its exit result. The new
locked source requires fresh `repair-v4-check` and real PostgreSQL
integration receipts plus independent final review.

### All-status Handoff original-source proof, not just Accepted

A third RED variant showed that a previously `InvocationCompleted`
Handoff child could skip all semantic frozen-input checks at terminal
Turn CAS: a self-consistent hash of a changed `childRunID`, or duplicate
status JSON, still allowed its stale terminal status to settle the Turn.
This was the same trust-source error across a different child status.

The shared `verifiedTerminalHandoffFrozenSource` now checks the complete
original `harnessRoot` seal, deterministic Started parent ID, unique
typed JSON and exact ChildRun ID. Both terminal and nonterminal Handoff
child branches call this same source validator before accessing their
separately appropriate completion evidence. A corrupted Handoff source
cannot evade verification simply by breaking the permissive legacy
child classifier: the reserved `hid_` parent namespace still requires
the strict original Handoff contract. The complete ancestor Attempt
list is then compared with the same Turn's current Invocation rows.
The normal nonterminal Handoff path still requires its independently
verified Started/terminal Delegation facts and matching physical
`handoff_start_returned` receipt.

Permanent RED→GREEN tests prove the status variants and self-consistent
rehashed ambiguous input; original local/async Role/Handoff positives
and full Harness + SQL module suites remain green. The completed
`repair-v4-check` is **historical** because it began before these
production changes; new `repair-v5` exact-source verification is
mandatory. No commit, PR or merge authorization.

### Locked repair-v5 local acceptance, November 10

Final **uncommitted, unpushed** Go-source aggregate SHA-256:
`ac35d227f84b8a68eeb4a75e207d6ab927de4d091acdb7bfeeab52f13163d43f`.

- `coverage/terminal-authority-final/repair-v5-check.exit = 0`:
  complete repository `make check` (Go fmt/tidy/vet/test/Race/Lint,
  TypeScript lint/typecheck/test/build, benchmark smoke, A2A/OTel
  product checks and Beta.24 clean consumer).
- `coverage/terminal-authority-final/repair-v5-integration.exit = 0`:
  actual PostgreSQL 16 and Redis acceptance, original Start/no-effect
  and post-crash two-connection Retry tests, Handoff and cancellation
  recovery, managed Session restart, and cross-process Continuation E2E.
  All disposable Docker test resources were cleaned.
- `repair-v5-baseline.log`: Agent protocol **131/131 PASS**.
  `repair-v5-quality.json`: offline fixed evidence suite **40/40 PASS**,
  160 synthetic Model calls/120 synthetic Tool calls; the original
  fixed dataset SHA-256 remained unchanged. No new paid Model run.
- `repair-v5-fuzz-*.log`: original Cancellation 21,370,
  Dispatch 19,829, Continuation 16,571 bounded fuzz executions,
  **57,770 total** without failures.
- Go Race `-count=2` on original frozen Handoff ancestry, stale
  intermediate Attempt, already-completed Handoff child and strict
  source JSON tests PASS. Independent temporary Go Overlay mutations
  that remove the terminal child's identity check or the frozen
  ancestor Attempt comparison turn their permanent tests RED; the
  normal candidate remains GREEN.
- Local Handoff/Delegation/Role/HostedTool positive paths and all
  Harness + SQL module unit suites PASS. Unsealed/malformed legacy
  Handoff child inputs are intentionally **fail-closed** pending
  explicit review/quarantine, with no compatibility adapter.

**Remaining release blockers:** an independent whole-chain final code
review of this now-frozen exact source and all GitHub required CI checks
on an eventual immutable PR Head. Current local verification is not
merger authorization; no commit, push, PR, merge or published release
has occurred. Unknown risks include privileged out-of-band writes,
unsupported custom Store semantics, paid live Provider replay, and
production-level performance under load.

### Independent V6 post-verification audit: type-specific sibling completeness (November 10)

The independent reviewer challenged the previous v5 green cohort with
conflicting children attached to original Host and Delegation sources.
The initial blanket hypothesis ("**any** different-Kind sibling means a
contradictory terminal result") was **falsified by existing legitimate
Handoff Artifact attachments**, and is not an accepted security rule.
The strict all-Kind change was reverted after existing real delegation
tests went red; benign application ItemArtifact attachments are allowed.

The actual original-authority contracts differ by type:

- **Host business receipt:** one and only one original Diagnostic response
  child (or a child claiming the reserved hbizverified_ identity) may be
  attached to the original Resolved Interaction source. The prior receipt
  reader counted only canonical receipt IDs and ignored a second Diagnostic
  sibling. Memory/SQL AppendItem must reject the alternate diagnostic before
  COMMIT; historical sibling corruption must fail closed on read/restart.
- **Handoff:** only ItemDelegation children are terminal delegation votes.
  Ordinary ItemArtifact siblings are not authority. The original shared
  validator already rejected two Delegation siblings, but the SQL terminal
  projection reader filtered Delegation facts by the *expected child Run*.
  A second Delegation child with a foreign Run therefore disappeared before
  the shared Memory/SQL predicate, letting SQL and Memory disagree. The
  SQL adapter now loads exact started IDs and at most two Delegation children
  per started ID independent of Run/Turn; malformed or duplicate terminal
  votes remain fenced.

The change retains the existing single immutable Item ledger, typed validators,
and Turn-lock atomicity. No new state machine, compatibility adapter, schema,
or unsafe global all-Kind rejection is introduced. Permanent independent
counterexample tests cover the shared Host verifier, Memory and SQLite
pre-COMMIT acceptance, a deliberately corrupted historical SQL row,
exact Handoff read parity, benign Artifact positive controls, and repeated
Go Race. A real PostgreSQL two-connection Host sibling regression is also
included in the integration suite.

**The V5 green receipts are HISTORICAL after these code changes.** V6 must
obtain a *new frozen source* make check and real PostgreSQL/Redis integration
pass, independent mutation sensitivity and final whole-chain review before
PR creation or merger. No release gate is waived.

### V7 follow-on: original-parent global identity is not Turn-filtered

A further adversarial overlay test replayed a historical *Diagnostic* sibling
whose ParentItemID referenced the original Resolved Interaction but whose
TurnID had been corrupted. The earlier SQL reader filtered by Turn before
authoritative verification and incorrectly allowed the fresh Runtime
Continuation Claim. This additional RED was not part of the V5 or V6 green
candidate and prevents accepting either as final.

The SQL write path and terminal-projection read path now share the same
bounded typed sibling loader: global parent Item ID lookup, maximum two
relevant children, no prefilter by allegedly trusted Turn/Run ownership.
Exact proof facts are loaded by globally unique ID and de-duplicated after
the two read paths converge. For a Resolved Interaction source only Diagnostic
or reserved hbizverified_ siblings are authority; ordinary Artifact attachments
remain accepted (including a 1300-attachment long-Turn positive fixture).
The expected Turn, Run, Item kind, payload, and sequence are validated after
lookup by the one shared Harness verifier. SQL append, SQL terminal status
projection, and real PostgreSQL cross-connection recovery have corresponding
regressions. All new fixtures are in test-only files and no schema change is
introduced. A staticcheck De Morgan rule in V6 was also corrected.

**V6 make check failed at the lint phase** (staticcheck QF1001) even though
unit and full Race phases passed. The frozen V6 source checksum and receipts
are not the V7 validation gate. V7 requires a new full check and real database
integration on its own frozen source before any delivery decision.

### V8 independent performance review: global original-parent read must be indexed

The V7 query read globally by `parent_item_id` in order to retain historical
siblings with corrupted `turn_id`. Independent performance review found the
Harness SQL Item model had **no index on that column**. Query `LIMIT 2`
constrained returned rows but **did not** constrain the database's global
search cost. The original V7 design could therefore cause full-table scans
under each original proof admission or terminal projection, and its 1300-Item
positive test measured returned materialization, not the database's effort.

A new independent RED regression on the untouched V7 Item schema proved
`idx_harness_item_parent` absent. The correction adds this normal B-tree
index to the GORM Harness Item model. New SQLite Stores now seek this index
for the global parent lookup according to `EXPLAIN QUERY PLAN`. A legacy
SQLite schema with its index removed is upgraded by an explicit, repeatable
`harnesspostgres.Migrate` call, retaining existing immutable Item records.
A real PostgreSQL isolated-schema/dual-connection test also asserts the index
exists in `pg_indexes` after the real migration. The same index serves
Handoff, Cancellation, Dispatch and Host siblings without introducing a
parallel receipt ledger or a second state machine.

**Breaking deployment/migration note:** the source contract has changed
from the V7 assertion of no new schema changes. Pre-release hosts must invoke
the existing explicit `Migrate` before serving requests that rely on global
parent proof checks. Installing this index on a large PostgreSQL Item table
may require an operational rollout/window; no concurrent no-lock migration
or production workload timing is implied by these local correctness tests.
Do not call this a zero-migration or proven constant-time change. Historical
noncanonical proof rows continue to fail closed and require explicit review.

**Status: V8 is locally uncommitted and unpushed.** The V7 all-green make check
and PostgreSQL/Redis acceptance receipts are historical because this model
migration changes production source. V8 must pass a fresh exact-source full
repository check, real PostgreSQL/Redis/Cross-process suite, targeted Race,
required mutation sensitivity and independent full-chain exit review before
staging or PR creation. Unrun paid models and custom Store implementations
remain external residual-risk boundaries.

### V9 repeated-Race fixture isolation after V8 acceptance

Although the **frozen V8 production source passed** the full repository
`make check` and real PostgreSQL/Redis/Cross-process integration, an
additional Go `-race -count=2` run exposed intermittent failures in the
**SQLite adversarial test fixture** `TestSQLInteractionAuthorityRejectsForgedFactsBeforeCommit`:
a canonical positive sometimes became `ErrConflict` after a deliberately
malformed negative had already been submitted. This was reproducible across
multiple Go `-count` runs, so the review did not waive it as noise.

A paired overlay experiment showed that **freezing the canonical Item before
mutating/submitting the negative** passed 65 repeated Race runs, while
copying only the negative's JSON payload but reconstructing the positive
*after* submission still failed repeatedly. The permanent fixture now
snapshots the canonical positive early and gives the negative an independent
payload buffer. This changes **test ownership/order only**, with no change
to Store validators, SQL reads, transaction boundaries or production APIs.
Direct V9 source test: **100/100 repeated Race executions passed** for the
previously intermittent test, and the scoped original SQL proof-adversarial
matrix passed three repeated Race executions.

The causal mechanism behind the original intermittent fixture behavior is
not independently proven; do not claim a production concurrency defect was
fixed based solely on this fixture change. Keep the original failure logs
and experimental overlays for further investigation if it recurs.
The earlier V8 full check/integration receipts are historical for the now
changed **test** source. Freeze V9 source and rerun the required local gates,
then require final independent PR Head CI before merging.

### Frozen V9 local acceptance and audit handoff — November 10

Final **uncommitted, unpushed** Go-source aggregate SHA-256:
`060622c0856eafd672127d24b9e77150fd19d38519f5293da6717b6d370ccf00`.

- `coverage/terminal-authority-final/repair-v9-check.exit = 0`:
  complete repository `make check` on the above frozen source: Go formatting,
  tidy, Vet, unit, Race and Lint across the workspace, TypeScript
  lint/typecheck/test/build, benchmark smoke, A2A/OTel acceptance, Beta.24
  clean consumer and Agent protocol **131/131 PASS**.
- `coverage/terminal-authority-final/repair-v9-integration.exit = 0`:
  real PostgreSQL 16 / Redis / cross-process Continuation E2E under Race,
  including real two-connection Host business proof, wrong-Turn historical
  sibling refusal, cancellation/Retry/lost-ACK recovery, original Handoff
  proof, and **real PostgreSQL `idx_harness_item_parent` migration**.
  The isolated Docker test project's containers, network and volumes were
  removed by the runner; unrelated Docker services were not touched.
- `repair-v9-quality.json`: fixed unchanged 40-task dataset
  `sha256:9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`,
  **40/40 scripted offline PASS**, 160 simulated Model / 120 Tool calls.
  No new paid live Provider benchmark was executed.
- `repair-v9-fuzz-*.log`: bounded original Cancellation 24,792,
  Dispatch 24,484, Continuation 20,309; **69,585 total**, no failure.
- `repair-v9-mutant-results/*.log`: six separate independent source
  overlays disabling Host read count, Host write admission, global SQL
  parent read, SQL terminal projection sibling read, cross-Run Handoff read,
  or the new parent-index declaration all made the targeted permanent
  regressions RED; the actual V9 source remained GREEN and unchanged.
- `repair-v9-sql-frozen-control-100.log`: the previously intermittent
  negative→positive SQL fixture passed 100 repeated Race runs after
  freezing its positive before creating the negative; `repair-v9-sql-race-repeat.log`
  passed three repeated Race executions over the authority-proof matrix;
  `repair-v9-sql-race-shuffle.log` passed two whole SQL package Race passes
  with shuffled test order. Original intermittent logs are retained as
  qualified residual evidence, not silently discarded.
- Independent semantic review examined the original identity chain from
  Grant/Resolved Host source through typed receipt, cancellation, claim,
  terminal Invocation/Turn CAS, retry, recovery and all Memory/SQL adapters.
  The concrete new P2 full-table-read performance blocker was fixed with the
  explicit index migration and verified by SQLite query plans, legacy schema
  upgrades and PostgreSQL schema checks. No new production source changes
  followed the V8 indexed proof correction; V9 only hardened test control
  isolation.

**Local gate disposition:** exact-source local technical checks **PASS**;
**formal PR/merge/release remains BLOCKED**. No commit, push, PR, merge,
release or main worktree alteration was performed. Independent review on the
final immutable PR Head, required GitHub CI, and migration rollout planning
for existing large PostgreSQL Item tables remain necessary. Residual
uncertainty is explicitly retained: the low-level mechanism behind the old
intermittent test-fixture behavior was not proven; privileged out-of-band
Store mutation, unsupported custom Stores, large-scale index-build/runtime
latency and paid live Provider replay were not verified. No unknown effect
or noncanonical historical authority fact is accepted via compatibility.
