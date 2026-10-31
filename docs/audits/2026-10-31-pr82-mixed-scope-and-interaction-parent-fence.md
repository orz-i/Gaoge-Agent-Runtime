# PR #82 second independent audit: mixed scope and Interaction parent fence

Date: 2026-10-31
Source HEAD: `3b1856581891387bbd2fd5017541f233435bc649`
Decision: [ADR 0015 §§39–40](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

The source HEAD passed all required GitHub checks before this new independent
source review. Green CI did not cover the following paths.

## Findings and corrections

1. **P1: native cancellation skipped native grandchildren across an Application
   intermediary.** Both CancelInvocation and CancelSubtask returned success and
   closed their intent while a native grandchild remained Running. Preflight
   found Invocation ancestry, but cleanup traversed only RunRelation. An
   Application edge intentionally need not have a Kernel RunRelation.

   `collectCancellationScope` now closes over RunRelation and frozen/Item
   ownership until no new owners or Runs appear. Preflight and physical cleanup
   share this exact scope. All typed child cancellation paths seed owned native
   descendants, cancel safe Application children and synchronize verified results
   before settlement. Missing relation capability may widen read-only admission
   checks, never physical cancellation of unrelated siblings.

2. **P1: a child Interaction callback could execute after its intermediate parent
   completed.** ResolveInteraction claimed WaitingInput-to-Running and called
   the Host handler without checking frozen parents. The subsequent error did
   not undo the already-issued effect.

   The callback now verifies parents before claiming and again after the claim.
   Final-boundary rejection proves this newly elected callback never ran and
   uses the existing no-effect reset. An existing Running unknown callback is
   not replayed or erased after parent termination.

3. **P2: conservative no-relation checks cancelled unrelated waiting siblings.**
   When Relations was absent, admission correctly inspected every owner, but
   then used that conservative set for WaitingInput-to-Cancelled CAS as well.
   A successful targeted cancellation changed an unrelated waiting Application.
   The read-only fallback remains conservative; mutable Interaction fencing now
   uses only the exact ownership closure. A three-run race red/green regression
   preserves the sibling's WaitingInput state.

## Evidence

- The two native-cancellation variants independently failed three times under
  Go race against the input HEAD; their permanent regression now passes and
  asserts unrelated sibling preservation and intent settlement.
- Interaction regression covers termination before claim, immediately after
  claim, a valid live parent, and preservation of a prior unknown callback.
- The older nested-business-callback fixture manually constructed an Invocation
  with nil Input and a placeholder hash. Its same root-owned parent Item now
  carries the valid root-sealed input; the RunRelation-only nesting and original
  cancellation assertions remain unchanged. Callback admission failures now
  fail the test promptly rather than hanging on a channel.
- Targeted combined regressions pass `go test -race -count=3`; full Harness
  tests pass and lint reports zero issues.
- Final source `6b9019b2eae4802372a95875af44aeae63beae9e` passes complete
  `make check`, `make coverage`, and `make integration` (all exit 0).
  The aggregate check includes unit/race/vet/lint and clean release consumers;
  integration exercises real Docker PostgreSQL and Redis.

Code commits: `2058562`, `9fc7b93`, and `6b9019b`. A separate post-push CI result and new
independent review are required; this record is not approval to merge or tag.
No new SQL DDL, Store API, receipt protocol, state enum or compatibility path.
