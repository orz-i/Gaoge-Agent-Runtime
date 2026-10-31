# PR #82 — Root liveness after physical child dispatch grant (P1 remediation)

- **Date:** 2026-10-31
- **Repository:** `orz-i/Gaoge-Agent-Runtime`
- **Branch:** `governance/harness-child-owner-indexed-20261031`
- **Original PR Head:** `36d428aaf662d9daf609fe645c456686a80a667d`
- **Finding:** Independent P1 audit, GitHub Review `5720141932`
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- **Disposition:** First ownerless-root P1 fixed on `05e7cf7`, but a second independent audit found a further cross-Root-Attempt P1 on that Head. See [subsequent frozen-Attempt hard cut](2026-10-31-pr82-child-root-attempt-hardcut-remediation.md). Independent merge-admission review must use the final pushed PR Head; no automatic merge or beta.24 tag.

## Root cause

A valid same-Turn parent Item may have neither `InvocationID` nor a
path of Item ancestors that names the owning root. The original
`requireLiveInvocationParents` walked only that Item ancestry and
checked Kernel execution state **conditionally** on encountering an
Item-owned Invocation. It returned success when the Item chain
contained no such owner, even if the original root Kernel Run had
become terminal after the earlier parent admission read.

The existing durable Feature physical grant protects concurrent
Harness cancellation, but by itself does not prove that the separately
owned Kernel Run is still alive. The Host Application child path uses an
Accepted→Running effect claim instead of the native grant, and had the
same gap in its post-claim check. The authoritative root cannot be
deduced from an arbitrary unowned Host Item.

## Fix

After the existing indexed ancestor walk, the same final
`requireLiveInvocationParents` additionally loads the **one durable
root Invocation** for `invocation.TurnID`. Missing, corrupt,
terminal or self-referential root identities fail closed. For a native
Runtime root, the check loads the exact frozen root `ExecutionRefID`
from Kernel and rejects missing, mismatched or terminal Runs.
Application roots have **no Kernel Run**: the root Invocation status
is their durable authority. No synthetic Kernel Run, new Store method,
replay protocol, persisted state, compatibility bridge or schema
revision is introduced.

This is the final **read-only** pre-effect check after the durable
dispatch grant or Application effect claim. It does not replace the
existing Store-serialized cancellation fence, child owner
verification, CAS or unknown-outcome reconciliation. In particular it
does not claim to make a cross-Store Kernel Cancel atomic with every
future remote Feature callback. Normal Harness cancellations are
coordinated by the original durable dispatch/claim boundaries.

## Deterministic reproduction and proof

- `TestP1ChildDispatchRejectsOwnerlessParentAfterRootKernelTermination`:
  a decorator cancels the root Kernel Run **after** the child
  `ExecutionDispatchParentItemID` grant is durably inserted, before
  the final check. On the original PR Head the test failed because
  `Team.StartRun` was called once. After the fix, the call count is
  zero; `ErrConflict` is returned, no child Kernel Run is created
  and the rejected pre-effect grant has a durable closing receipt.
- `TestP1ApplicationChildDoesNotEffectAfterOwnerlessParentRootTermination`:
  a decorator cancels the root Run **after** the Application child's
  Accepted→Running CAS succeeds. Originally the external Executor was
  invoked once; now it is not invoked and the child records its
  definitive no-effect failed outcome.
- `TestOwnerlessParentWithLiveRootCanStartChild`: valid unowned Item
  with live root is still accepted and starts exactly one child.
- `TestRootApplicationParentFenceUsesInvocationNotKernelRun`: live
  Application root passes without any Kernel Runtime; cancelling its
  Invocation blocks the child.
- `TestMissingRootCannotAuthorizeOwnerlessChild`: a missing root
  blocks an otherwise valid ownerless Item.
- Existing indexed Item ancestry, nested parent cancellation,
  Application retry after post-claim termination, and tool receipt
  tests are retained.

## Acceptance gates

The final verification is tied to the code and tests above and must be
completed before pushing the correction:

- Harness targeted regressions and `go test -race ./... -count=1`.
- Full `make check` on the **frozen final source**, including
  Go race/vet/lint and TypeScript/clean-consumer checks.
- `make integration` using real Docker PostgreSQL/Redis.
- `git diff --check`, exact branch/Head check and preservation of
  the two preexisting, unrelated untracked Responses probe scripts.

GitHub branch protection and new-Head CI/review remain independent
from these local gates. The original PR Head's earlier CI passes do
not certify this correction.
