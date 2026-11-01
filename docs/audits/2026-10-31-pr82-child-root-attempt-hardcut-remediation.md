# PR #82 — Frozen child root Attempt hard cut (second independent audit remediation)

- **Date:** 2026-10-31
- **Repository:** `orz-i/Gaoge-Agent-Runtime`
- **Base PR Head:** `05e7cf75b02aea12851d901defa8805c1b0e8f03`
- **Branch:** `governance/harness-child-owner-indexed-20261031`
- **Disposition:** Code remediation prepared locally; final merge admission must independently review the new pushed Head.
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

## Reproduced regressions

The earlier PR #82 root-liveness patch rejects a child after its root
Kernel Run becomes terminal, but it checked the **current** root identity.
A top-level Invocation survives a root `RetryInvocation`: its ID is stable,
while `ExecutionRefID` and `Attempt` rotate. An Accepted child committed
under root Attempt 1, followed by a lost ACK/crash and a root retry, could
then pass root-liveness against live Attempt 2 and execute its old physical
`Team.StartRun`. Both a same-Turn ownerless `ItemArtifact` and an
`ItemInvocation` that explicitly recorded the old root Run reproduced
the same unintended side effect (`calls=1, err=nil`) on Head `05e7cf7`.

A separate true-entry regression showed that a live Application root was
permitted by the internal liveness check but rejected at
`StartApplicationInvocation` admission: `childInvocationContext` attempted
to load its host-only `ExecutionRefID` as a Kernel Run.

The newly added final root-liveness check also called
`loadTopLevelInvocation -> Store.ListInvocations` at *every* physical
child effect boundary, materializing an unbounded number of child
Invocations when only the frozen root was needed.

## Correction

1. **One immutable root Attempt proof.** The initial child
   `CreateInvocation` commits a reserved `harnessRoot` object in its
   existing hashed `Invocation.Input`:
   `{invocationID, executionRefID, attempt}`. Only the Harness seals
   it; input with a preexisting reserved field is rejected. Both ordinary
   Team/Workflow/Application child admissions and first-party delegated
   Role Agent creation use the same helper.
2. **No rebinding after retry.** Idempotent child admission retains the
   original `InputHash` and fails on a later root Attempt. Before an
   actual physical child dispatch or Application external-effect claim,
   the existing ancestor fence additionally loads the frozen root by
   `Store.GetInvocation(rootID)`, compares exact Run/Attempt identity
   and nonterminal status, and loads its Kernel Run if applicable.
   Child retry and resume share this boundary. Missing/malformed binding
   fails closed, even if a new root is currently live.
3. **No full-list root read at dispatch.** Exact indexed
   `GetInvocation` replaces `ListInvocations` in the final fence.
   The admission-time lookup of the currently selected root is unchanged;
   this correction makes no unsupported whole-Turn performance claim.
4. **Host Application root owns Host children.** A live claimed
   Application root can admit an Application child using the frozen
   Invocation identity, without synthesizing a Kernel Run. A native
   Team/Workflow child still requires a Kernel-backed root and is
   rejected under a Host-only Application root.
5. **No alternate executor/state protocol.** The existing durable
   dispatch grant, Application CAS claim, cancellation fence, unknown
   external outcome handling, Kernel CAS/Epoch and immutable Run IDs remain.
   No SQL DDL, persistence-version churn or compatibility bridge.

## Regression evidence

- `TestChildAdmissionAndRetryCannotRebindToNewRootAttempt`: ownerless and
  root-owned parent Items, a simulated lost child `CreateInvocation`
  acknowledgment, root terminalization, root Attempt 2 with a new live
  Run, and rejected child replay **and retry** without new execution.
- `TestChildFinalEffectFenceUsesExactFrozenRootLookup`: 1,000 extra
  Invocations; a Store decorator forbids `ListInvocations` on the final
  fence and verifies one exact primary-key root read.
- `TestChildFrozenRootProofIsMandatoryAndImmutable`: legacy missing
  proof, mutated input hash and resealing a frozen child are rejected.
- `TestApplicationRootAdmitsApplicationChildWithoutKernelRun`: the
  real entry path invokes one Host Application child successfully, while
  a native Team child under that same Host root is refused.
- `TestRoleDelegationFreezesExecutionAndProjectsChildApproval` and
  original PR #82 post-grant/post-claim cancellation races remain green.
- `BenchmarkChildRootFinalFenceIndexed`: memory-only 500 / 2,000 /
  10,000 Invocation case; no PostgreSQL or whole request speed claims.

## Local validation on the finalized implementation

| Gate | Result | Evidence |
| --- | --- | --- |
| Targeted P1/P2 and original PR #82 Race regressions | PASS | `go test -race ./go/agent-runtime-harness -run 'Test(...)' -count=1` |
| Full quality / race / lint / clean-consumer gates | **PASS, exit 0** | `make check`; `/tmp/gaoge-pr82-root-attempt-check-final.log` on Mac Pi |
| Real PostgreSQL + Redis recovery / integration | **PASS, exit 0** | `make integration`; `/tmp/gaoge-pr82-root-attempt-integration-final.log` on Mac Pi |
| Diff whitespace | PASS | `git diff --check` and staged diff checks |

Apple M4, in-memory `BenchmarkChildRootFinalFenceIndexed`, 80 iterations
per case: 500 / 2,000 / 10,000 unrelated Invocations yielded
approximately 4.1 / 1.6 / 1.4 μs per fence, respectively, at 11
allocations per operation. This is only an indexed in-memory microbenchmark,
not proof of PostgreSQL or whole-request latency.

## Upgrade and acceptance

**Breaking persisted JSON:** The `Invocation.Input` shape changes for
new child and Role-delegation records, though the existing SQL table and
stored schema version do not change. Existing nonterminal child records
without the exact root Attempt proof cannot be resumed or redispatched;
pause admission and drain or isolate them before deploying this
unpublished candidate. Never reconstruct proof from the latest root,
rotate an old child under a new root, or reissue an unknown effect.

The local validation results and any new pushed PR Head's required CI
are separate gates. These notes are not a GitHub APPROVE or merge/tag
authorization.
