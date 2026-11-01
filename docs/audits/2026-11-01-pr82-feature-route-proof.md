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
