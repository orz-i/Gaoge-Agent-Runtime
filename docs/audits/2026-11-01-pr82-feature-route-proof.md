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
