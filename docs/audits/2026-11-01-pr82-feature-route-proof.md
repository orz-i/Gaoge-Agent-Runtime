# PR #82 — Feature route and recovery receipt proof

## Scope and findings

Independent review of PR Head `5af89fd959cd92eed72dcda07ddefc61c7b19a98`
recovered the unfinished local remediation and reproduced these failures:

1. **Raw Feature descendants:** a Team's external member can share a Run ID
   with an unrelated local Kernel Run. Root and Subtask cancellation previously
   cancelled the local twin, declared cleanup complete and left the actual
   member running. Subtask reads could expose the unrelated local Goal/result.
2. **Delegation Tool recovery:** a remote/dynamic child route or a foreign task
   with the same ID could obtain an autonomous local wait and committed Tool
   receipt from a local terminal twin.
3. **Subworkflow recovery:** an existing unrelated Workflow at the deterministic
   child ID could be resumed and accepted as the effect result. Create-conflict
   recovery also needed immutable origin validation before receipt projection.
4. **Independent review of the proposed repair:** a global Harness Invocation
   lookup initially bypassed raw Feature proof. An unrelated native Invocation
   in another Turn (or a sibling in the same Turn) cannot authorize that edge.

## Correction

- Harness proves every raw Feature relation back to an owning Invocation.
  Team and GroupChat attest their exact fixed same-Kernel ChildRunner, persisted
  member/speaker delegation and child envelope. Workflow attests only its
  built-in subworkflow executor, immutable definition/input and effect origin.
- Raw Feature edges take precedence over global Invocation lookup. Relation
  read failures and unknown routes never turn into successful local authority.
- Cancellation checks ownership before accepting even a terminal local twin.
  Failed proof retains the existing durable cancellation intent. Read projection
  keeps persisted relation/delegation facts, rather than borrowing local output;
  Subtask approval rechecks the same proof before dispatch.
- Delegation Tool receipt recovery, native-result projection and autonomous
  waits use fixed route plus frozen child task identity. Remote output cannot
  borrow a local twin's Agent type.
- Built-in subworkflows validate the exact child ID, kind, Actor/Thread,
  RequestID/Goal, definition version/hash, nested depth and input before Resume
  or accepting a recovered Create-conflict result.

No Store interface, SQL schema, persisted version, router or compatibility
fallback is added. Custom/dynamic/remote Feature children remain unverified for
local operations; the actual execution owner must reconcile them. Existing
synthetic cancellation fixtures were replaced with genuinely local Team member
execution, preserving positive-path coverage rather than weakening the proof.

## Evidence

- Go overlays restoring the relevant Head implementations reproduced the first
  three families as assertion failures, not compilation failures: remote member
  still Running after reported successful cancellation; foreign Goal projected;
  unverified local wait/committed Tool receipt; unrelated Workflow advanced from
  Running revision 1 to Completed revision 2.
- A separate reviewer independently reproduced the global Invocation bypass in
  foreign, identical-metadata and terminal-twin variants. The corrected proof
  passed that unchanged overlay with `-race -count=3`.
- Permanent regressions cover no Invocation, other-Turn and same-Turn unrelated
  Invocation owners; fixed-local versus remote/dynamic/foreign-task delegation
  receipts; subworkflow loaded-child and Create-conflict paths. Focused Harness
  and all three Feature proof suites passed `-race -count=3`.
- Independent read-only reviewers found no further substantiated issue in the
  corrected route proof, delegation recovery or subworkflow dispatch changes.

- Final-code `make check` passed (exit 0), including unit/race/lint, TypeScript,
  evaluation/benchmark smoke, A2A checks and clean release consumers.
- Final-code Docker PostgreSQL/Redis `make integration` passed (exit 0), with
  containers and network removed by the integration runner.

Exact pushed-Head CI and subsequent independent audit are separate merge gates,
recorded in the completion report. This record does not authorize merging,
tagging or releasing.

## Post-CI independent audit: additional ownership boundaries

After all required checks passed on `c283579`, two fresh reviewers found three
additional gaps and reproduced them independently:

- A routed Handoff ancestor's same-ID local twin could satisfy the frozen
  parent liveness check and authorize a nested Host Application callback.
  The shared admission/final-claim check now requires fixed route-owned,
  frozen task observation for Handoff ancestors. Member/Role, foreign and
  identical twins, dynamic routes, and late route changes are covered, while
  a genuine fixed-local ancestor remains valid.
- An unrelated Accepted Handoff Invocation with no physical grant could lend
  no-effect proof to a raw Team child before the relation check. The shared
  no-effect helper now verifies any existing relation belongs to that original
  delegation. Missing Runs, same-/other-Turn owners and terminal twins retain
  unverified cleanup instead of claiming success.
- Subtask approval proved only local readability, then invoked the root
  AgentStarter. A distinct root wrapper was observed to execute the delegated
  approval callback. Physical approval now additionally requires the exact
  fixed first-party Agent object used by Handoff/Team/GroupChat. Sharing a
  Kernel or providing a currently matching dynamic route is insufficient.
  Unknown/substitute routes reject before a dispatch grant. Native Invocation
  approval retains its configured Agent; the original fixed child path passes.

Both reviewers validated the repairs against their reproductions and permanent
positive/negative regressions with `-race -count=3`, finding no further
substantiated issue in those changes. These deliberate pre-stable behavioral
hard cuts add no remote continuation bridge or persisted migration. Hosts must
retain original static composition across recovery, and route approvals through
the actual owner rather than substituting another local Agent.

The second-cycle final code passed complete `make check`, `make coverage`, and
Docker PostgreSQL/Redis `make integration` (all exit 0; integration cleanup
complete). The next pushed SHA still requires its own CI and fresh audit.

## Second post-CI audit: raw Feature Canceller ownership

After required CI passed on `1ff509f`, another fresh full-boundary review found
that raw Feature cancellation proved the ChildRunner's Kernel but still called
an arbitrary custom Canceller. A public `CancelSubtask` reproduction invoked a
Canceller explicitly bound to another Kernel and cancelled its unrelated twin;
post-callback receipt validation was too late to prevent that effect.

Raw Feature control now requires the same exact local Canceller ownership proof
as direct Handoff cancellation, before any callback. Unknown/custom foreign
ownership preserves unresolved cleanup. The unchanged independent reproduction
and permanent foreign-Kernel/unproven-local/owned-local positive tests passed
`-race -count=3`; full Harness tests and lint also passed. No additional
admission/Store issue was found in that round. The reviewer independently
validated this repair, and final-code `make check` plus real PostgreSQL/Redis
`make integration` both passed (exit 0, integration cleanup complete).

## Subsequent PR #82 independent audit: unindexed composite cancellation P1

At PR Head `532b9a6`, independent Go Overlay probes reproduced two outcomes
three times each with the race detector: a Running Team or already-Failed Team
could physically start a remote ChildRunner member without `Relations`, then
`Runner.Cancel` returned success and released the cancellation fence while
that remote member remained Running. A second configuration split (Team writes
to one recorder; Harness reads another) had the same empty-index ambiguity.

The repair makes `relatedChildRunIDs` reject any **Harness-owned composite**
with an existing Kernel Run if the Harness index is absent, or if a first-party
Team/GroupChat/Workflow runner is not connected to the identical recorder.
Original Invocation execution class, not a same-ID local foreign Run kind,
determines which composite source is relevant; raw Feature and Handoff children
keep their existing separate route proof. A definitively absent Kernel Run
retains the original no-effect boundary. Custom Feature adapters still own
their Host-supplied relation-writing contract.

Permanent regressions cover Running/Failed, absent/divergent index, repeated
Cancel, original remote member liveness, and retained durable intent. Memory
and SQL Store tests cover the identical cancellation-fence semantics; the real
PostgreSQL test runs when `TEST_POSTGRES_DSN` is available. This correction
changes no Store API, SQL DDL, Kernel state, physical retry protocol or
compatibility path. Its own exact-Head CI and independent review remain
required before PR merge.

## Next independent review: optional proof bypass and cross-Kernel native route

Review of pushed Head `796a202` found two new P1 cancellation-receipt
failures. Both were independently reproduced with temporary Go Overlays,
`go test -race -count=3` (3/3 reproductions each), without changing tracked
code:

1. A public Host TeamFeature wrapper forwarded StartRun/Resume to a Team
   without forwarding its optional `RecordsRelationsTo` method. Harness had a
   different empty relation index, yet `Cancel` succeeded and closed the
   cancellation intent while an actual remote member stayed Running. The
   `if ok && !proof` condition treated a missing method as positive authority.
2. A native Workflow runner and Harness shared one exact relation Registry,
   but executed through two independent Kernel instances. The Workflow
   remained WaitingInput in the foreign Kernel while Harness returned
   `TurnCancelled`, released the intent, and never cancelled that physical
   Run. Registry identity was incorrectly used as a substitute for Kernel
   residence.

**Correction:** The original composite Feature must attest both its exact
physical Kernel via `OwnsKernelRuntime` and its exact relation recorder via
`RecordsRelationsTo`. An absent method, false proof or different Kernel
fails closed before physical cancellation and preserves the committed
cancellation intent. Concrete built-in Team, GroupChat and Workflow runners
expose the two narrow proofs, and `NewRunner` rejects a foreign concrete
native composite Kernel at composition. Raw Feature edges with unrelated
Harness Invocation collisions keep their separate incoming relation and
fixed-route verification. A missing local Run can discharge an original
composite only with a definitive pre-effect rejection or an Accepted child
with no physical grant; a failed/terminal parent or settled physical Start
is not no-effect proof. No schema, Store protocol, new router or compatibility
branch was added.

Permanent tests cover both original scenarios, all three native constructor
paths, the exposed Host wrapper routes, and retained cancellation fences on
Memory/SQL/PostgreSQL. Existing native and custom Kernel no-effect read
boundaries remain unchanged; the test-only Team fixture explicitly attests
its true same-Kernel and no-descendant contract. A repeated Cancel on a
still-Running parent with unresolved intent may legitimately return
`ErrConflict`, never a successful cancellation.

The patch requires fresh final-SHA CI and an independent post-fix merge
admission review. No merge or beta.24 release is authorized by this record.

## Subsequent independent Head audit — native Subworkflow relation-after-start P1

At `f95e035`, the reviewer independently injected a durable
`KindWorkflowEffect` write failure into the original native Workflow's
RunRelation registry. A registered Subworkflow child was **physically
created first**, persisted as `WaitingInput` and then the relation insert
failed. The parent transitioned to `Failed` with no child edge, and Harness
`Cancel` returned `nil` without a durable active cancellation fence while
the child remained `WaitingInput` (3/3 runs under `-race`). A matching registry
object only proves where writers send their relations; it cannot establish
that the native writer committed the relation before the physical effect.

**Correction:** The native Subworkflow executor now derives the child ID
from its persisted Effect and commits the exact immutable parent/child
relation before creating a child, resuming an existing child or trusting its
terminal receipt. The shared `ensureEffectRelation` writer preserves the
original Effect's Attempt/fan-out ownership. `EffectRequest.Mapped` explicitly
carries the frozen mapping flag instead of inferring it from `MapIndex` or
owner IDs. Invalid or missing definitions do not reserve orphan relations
before the definition is resolved for a new child. A rejected pre-dispatch
Ensure starts no child. A persist-success/lost-reply Ensure also starts no
child but retains conservative unknown-cancellation handling for any
committed orphan edge; it is never treated as proof that a physical child
stopped. A retry with a successful durable edge reuses the same child ID.

Permanent Memory/Harness tests assert the relation is visible inside the
physical Kernel Store `Create`, including successful pending child
cancellation, replay idempotence, rejected inserts, committed-lost replies,
reconstructed Harness cancellation and retained intent. A real PostgreSQL
Kernel+RunRelation test covers the same three cases and reconstructs both
readers using a fresh PostgreSQL connection. PostgreSQL/Redis integration and
exact-Head full quality checks are required again. Neither an automatic
backfill for candidate-era orphaned children nor a new Store/SQL protocol
was introduced; legacy orphans must be reconciled explicitly by the
original execution owner before treating cancellation as complete.

This audit record does not grant PR merge or beta.24 publication.
