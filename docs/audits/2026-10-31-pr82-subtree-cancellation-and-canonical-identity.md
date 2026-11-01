# PR #82 independent subtree cancellation and canonical identity audit

Date: 2026-10-31
Source HEAD: `0ba1fb174ceda1ca9c803a2c7f04f0bd3f8b759b`
Decision: [ADR 0015 §§36–38](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

## Independently reproduced findings

1. **P1: direct external RunRelation Subtask cancellation falsely succeeded.**
   `cancelRuntimeRun` treated local `ErrNotFound` as no-effect, while the
   relation proof only ran for traversed descendants. Direct target cancellation
   returned nil and closed the intent while its actual ChildRunner stayed Running.
2. **P2: terminal Subtask status erased failed subtree cleanup.**
   `mergeSubtaskCancellation` converted a failed request to `confirmed` and erased
   its error when the parent was Failed/Completed/Cancelled, despite an unknown
   external descendant. Parent execution status is not a cancellation receipt.
3. **P2: valid Handoff requests stranded physical children after normalization.**
   Goal, ChildRunID and a trailing Delegation ID can contain whitespace accepted
   by Handoff validation. Kernel canonicalized the physical request, then the new
   exact comparison rejected that same child in StartOrLoad and Refresh.
4. **P1: terminal Application root cancellation ignored unresolved children.**
   A real public Application root executor admitted a child whose external result
   was lost; the root completed, and Cancel returned nil while the child remained
   Running. Application-root nesting is newly enabled by this PR.
5. **P1: intermediate Application cancellation had the same subtree bypass.**
   Under a native root, an Application intermediate completed while its actual
   nested Application effect remained Running. CancelInvocation returned nil.

Each independent original reproducer failed three times with `go test -race`.
The reproductions are retained as permanent regression tests, not merely audit
claims. No production credentials, remote effects or fabricated remote receipts
were used.

## Corrections and positive controls

- Missing local RunRelation proof is enforced by the shared physical cancellation
  entry, including direct Subtask targets. Budget cancellation alone cannot prove
  an external Run stopped. Unknown outcomes retain the existing intent.
- Subtask confirmation is recorded explicitly only after target and complete
  descendant cleanup. Failed, Completed and Cancelled parents preserve unknown
  cleanup errors. Positive local cleanup and budget-fenced local cleanup confirm.
- Application roots use the existing Turn-wide admission/Interaction fence.
  Intermediate cancellation targets frozen ancestor/Item-owned descendants,
  cleans native descendants through existing controls and leaves siblings alone.
  Running Application effects fail closed; accepted claims compete with the
  cancellation intent. Terminal Application identity can recover the same pending
  intent without inventing a Kernel Run. Completed/Failed results are preserved.
- Handoff verification compares the returned fields with Kernel-canonical request
  ID, Goal and composite RequestID, never normalizes returned snapshots, and keeps
  exact Actor/Thread, status, kind and route ownership checks.
- Additional deterministic races cover claims immediately before and after intent
  admission. A claim winner blocks cancellation; an intent winner blocks the late
  claim. Settlement failure can resume from exact terminal Application authority.
- Mixed Application/native subtree tests verify both child and RunRelation leaf
  cancellation while an unrelated native sibling remains Running.

## Verification

Focused independent red/green and positive-control tests pass with race enabled
and three repetitions. Code commits: `d348e66` (Harness) and `52dcd64`
(Handoff). Complete `make check` on the committed source exited 0, including
Go unit/race/vet/lint, TypeScript, scenario checks, A2A and the beta.24 clean
external consumer cohort. `make coverage` exited 0. Real Docker PostgreSQL/Redis
`make integration` exited 0 and cleaned its test containers/network.

Local evidence: `/tmp/pr82-round1-committed-check.log`,
`/tmp/pr82-round1-coverage.log`, `/tmp/pr82-round1-final-integration.log`.
Earlier intermediate check attempts caught a temporary edit-window compile
failure and test import formatting; neither is counted as passing evidence.
Both were corrected before the complete committed-source pass.

The exact pushed HEAD must separately pass GitHub CI and a fresh independent
audit. This document is not merge approval.

No SQL DDL, Store API, state enum, remote Cancel RPC, compatibility bridge, release
or tag was introduced. The two pre-existing untracked OpenAI Responses probe
scripts are unrelated and remain untouched.
