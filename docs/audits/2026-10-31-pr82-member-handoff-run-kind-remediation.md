# PR #82 — Member Handoff physical-fence and child Run-kind remediation

- **Date:** 2026-10-31
- **Independent reviewed Head:** `5ab968e7d98b878fc7945b82ddc734080164c151`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- **Scope:** pre-stable beta.24 candidate; **not** authorization to merge or publish.

## Reproduced defects before the fix

1. **P1 — MemberID-only Handoff bypassed the child grant.** In
   `executeDelegation`, `RoleID == ""` still invoked
   `HandoffStarter.StartOrLoad` directly, while Role-based delegation alone
   received a durable child-owned grant and post-grant ancestry check.
   Independent Go Overlay cases returned a physical Handoff call **after
   terminal parent status** and **after pending cancellation intent**.
   The public `Runner.Delegate` entry reproduced one external call after
   its durable Started Item ACK despite parent termination.
2. **P2 — unrelated Kernel Run kinds were accepted as Agent children.**
   `handoff.verifiedChildRun` checked child ID and lifecycle status only.
   A matching `workflow` Run with `completed` status was projected as a
   completed Agent Delegation on LoadRun, StartRun and Refresh. Negative
   tests also cover missing, Team, GroupChat and unknown kinds.

## Correction

- Use **one child Invocation path** for every Handoff, regardless of whether
  the caller selects `RoleID` or `MemberID`. The child seals the original
  root and all transitive parent Attempts into immutable, hashed Input.
  `dispatchHandoff` obtains the original `handoff-start` dispatch grant,
  then revalidates the entire bound ancestry before any external
  `StartOrLoad`. No old direct Member dispatch or compatibility bridge.
- Handoff accepts exactly `agent.RunKind` and the explicit
  `handoff.RemoteAgentRunKind` for an A2A remote-Agent shadow, with matching
  Run ID and recognized lifecycle status. The A2A adapter reuses that one
  constant. Invalid results cannot be projected as a valid Delegation or
  settle a grant.
- A still-executing `StartOrLoad` without a verified returned child
  snapshot retains its physical grant. Cancellation cannot claim the
  external start has stopped just because a local Run happens to be visible;
  a cancellation attempt against this unresolved grant must fail closed.
  Once a child start returns verified evidence and the grant is settled,
  normal descendant cancellation and read-only status projection remain
  available. The former synchronous member cancellation test now checks
  this actual authority boundary; the asynchronous acknowledged-child tests
  cover normal cascade and external cleanup retry.

## Regression proof

- New durable tests: `TestPR82PublicMemberDelegationFencedAfterDurableStartedItem`,
  `TestPR82MemberHandoffFencedAfterChildCreate`, and
  `TestPR82MemberHandoffUnknownOutcomeRetainsGrant`.
- New Run-kind tests:
  `TestPR82HandoffRejectsWrongChildRunKind` (Load/Start/Refresh,
  including unknown/empty kinds).
- Verified original independent Go Overlays now **PASS** against fixed
  source for public Member cancellation, internal parent/cancellation races,
  and wrong Run Kind.
- Existing Role Handoff tests, real A2A ShadowRunner test, asynchronous
  child completion/failed/cancelled projection, and pending-grant
  cancellation semantics remain covered. The asynchronous ChildRunner test
  fixture now emits the actual `agent.RunKind`; its custom canceller
  identifies the exact child Run ID, not a fabricated non-Agent kind.
  `TestHarnessCancelRefusesUnsettledDelegationDispatch` proves an
  in-flight Handoff start prevents a falsely successful parent
  cancellation, whereas acknowledged asynchronous child Run tests prove
  normal cancellation and cleanup can proceed.
- Full `make check`: **PASS, exit 0**, against the final reviewed code in
  commit `23c4da8`. Includes all Go modules, race tests (including
  Harness/SQLite), vet, lint (zero issues), TypeScript lint/tests/build,
  A2A product gate, release checks, and beta.24 clean-consumer cohort.
  Evidence: `/tmp/gaoge-pr82-member-kind-final-v3.check.log` on Mac Pi.
- Full `make integration`: **PASS, exit 0**. Real PostgreSQL/Redis
  tests, crash/recovery, cross-connection cancellation and continuation E2E
  passed; the Docker test containers/network were removed. Evidence:
  `/tmp/gaoge-pr82-member-kind-final-v2.integration.log` on Mac Pi.

## Recovery and rollout boundary

An ambiguous `handoff-start` outcome remains bound to the original
`Invocation.ID`, `ExecutionRefID`, parent Attempt chain, and durable
grant. Operators must inspect the original ChildRunner route and Run
**read-only** and either verify the authoritative result or quarantine the
unresolved delegation. An absent Run or network error is not no-effect
proof. Do **not** close the grant, rotate an Attempt, retry the same
physical start, or mark cancellation successful without an actual
authoritative receipt. There is no automatic legacy replay/fallback or
new Store interface/schema/Kernel status.

Old Member-only pending delegations without a sealed child Invocation
cannot be adopted by this candidate's grant model. Drain them before
rollout or explicitly quarantine them. Published beta.23 tags remain
immutable; run a separate merge-admission audit on the final pushed Head.
