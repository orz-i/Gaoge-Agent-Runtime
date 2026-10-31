# PR #82 — Composite physical Start / cancellation admission hard cut

- Date: 2026-10-31
- Independently audited Head: `1610d6c7e3ff1081e13b58262b7816565949636e`
- PR: [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- Decision: [ADR 0015 §25](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- Scope: One P1 Root Cancel vs. in-flight Team member physical start.

## Independently reproduced Red

A real Harness `StartTeamTurn` created its parent Team Kernel Run, then paused on a RunRelation `Ensure` BEFORE calling the remote Member `ChildRunner.StartRun`. The physical Start grant was durable but still unresolved. Concurrent `Runner.Cancel` read the existing Team parent Kernel Run, skipped the pending grant via the generic "Run exists" exemption, successfully committed `TurnCancelled`, then the paused Start resumed and physically issued a new remote Member execution.

- Detached worktree Red: `/tmp/pr82-reaudit-cancel-team-cascaded-race-20261031.log`
- Repeated `go test -race -count=3`: all three attempts demonstrated `Cancel status=cancelled err=nil` followed by one new `ChildRunner.StartRun`.
- Source reproducible in Mac Pi: `/tmp/pr82-reaudit-cancel-team-cascaded-repro_test.go`.

## Correction

`Runner.pendingExecutionDispatch` now considers every unsettled composite Team/Workflow/GroupChat Start or Start-Retry grant as physically in flight until one exact grant settlement receipt commits, regardless of whether that Feature has already created a parent Kernel Run. This applies to both root `Cancel` and descendant `CancelInvocation` / `CancelSubtask` through their shared related-cancellation fence. With an unresolved composite Start, `Cancel` fails closed with `ErrConflict` and does NOT claim that the external child has stopped.

Only a concrete first-party `*agent.Runner` for an exact matching *root* Agent Invocation, `agent.RunKind` Kernel Run, original physical Run ID and the **identical `*kernel.Runtime` RuntimeCanceller** can use the existing Kernel cancellation handoff. Its native continuation is guarded by Kernel CAS, and by a fenced execution Epoch if one was acquired; unexpected Kernel/Store verification failures keep the grant pending. Custom Agent adapters and all composite Features have no such exception. This is an explicit pre-stable behavior hard cut, not an implicit fallback mode.

The correction uses existing immutable Dispatch Grants and Turn admission/cancellation fences. No new network cancellation API, protocol, Store/RunRelation interface, schema, DDL, version bump or compatibility bridge. A failed Cancel can be retried after the original physical Start completes or the unknown outcome is explicitly reconciled, not by inventing a completion receipt.

## Tests and release gates

- Public Harness true `StartTeamTurn` cancellation race: `TestPR82CancelCannotPrecedePhysicalTeamMemberStart`. Red on audited Head, Green when `Cancel` is rejected until Member Start settles.
- Memory, SQL/SQLite, and real PostgreSQL Turn-lock integration: `TestPR82CompositeStartGrantFencesCancelAcrossStores` and `TestRealPostgresPR82CompositeStartGrantFencesCancel`. Tests deliberately compose a genuine first-party native Agent as another dependency to ensure a Team cannot inherit its special cancellation permission.
- Existing `TestCancelWhileAgentIsRunningConvergesBothCallersToCancelled` still passes: native Agent Model-in-flight can be fenced by its own Kernel CAS, preserving pre-existing Agent behavior.
- `TestPR82NativeAgentWithCustomCancellerKeepsUnsettledStartFence`: an arbitrary Host canceller cannot use the first-party Kernel exception, even when an actual native Agent and matching Agent Run are present.
- Full `make check`, `make integration`, independent exact pushed-Head Race and GitHub CI remain release/merge gates; this document is neither a merge authorization nor a Tag proposal.

Two unrelated untracked `scripts/probe_openai_responses.py` and `scripts/test_probe_openai_responses.py` remain untouched.
