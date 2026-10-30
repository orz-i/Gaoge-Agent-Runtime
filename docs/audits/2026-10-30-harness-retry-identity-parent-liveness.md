# Harness Retry frozen identity and ancestor liveness — local acceptance

- **Date:** 2026-10-30
- **Base:** merged `main@c0f331a7520fcca019bbb1ae254cdee5863af9d0` (PR #75)
- **Branch:** `governance/harness-retry-identity-parent-fence-20261030`
- **Reviewed implementation Head:** `5a920ac75fb2a387b933025b721fe84bd5c114d8`
- **Outcome:** Local **GO for protected PR and independent final-Head review**.
  Not permission to merge, publish beta.24 or silently re-admit in-flight work.

## Before/after reproduction

1. **Missing root identity, `72af538`.** Previously,
   `topLevelInvocationIdentity` accepted retryable root Invocation input
   missing its frozen Actor and/or Kernel Thread. It reconstructed the
   physical Run's authority from the Session Actor and product HostThread.
   This was not an immutable Kernel Thread identity: a Session HostThread
   can intentionally differ from the originally authorized Runtime Thread.
   The old code retried successfully in all three negative fixture cases
   (Actor missing, Thread missing, both missing), even though the exact
   Invocation Input was correctly hashed. That silently changed execution
   identity after the prior Run failed.

   The old Session fallback was deleted. `frozenTopLevelInvocationIdentity`
   reads only the hash-verified Invocation Input; invalid/missing authority
   now returns `harness.ErrConflict` **before** Store `RetryInvocation`
   advances Attempt, ExecutionRefID or reopens the Turn. The physical
   retry path validates again after the CAS. Valid top-level typed
   Workflow and Application retries remain supported.

2. **Terminal child ancestor, `5a920ac`.** Previously, public
   `RetryInvocation` validated the product Turn but did not check
   parent Runtime/Invocation liveness before rotating a failed child's
   Attempt. The eventual `startRetriedInvocation` would detect a
   cancelled root Kernel Run or terminal intermediate Harness Invocation
   and return `ErrConflict`, leaving a newly persisted,
   unreachable `InvocationAccepted` attempt.
   Two deterministic negative fixtures proved this exact incorrect
   post-error persisted state.

   Retry now checks the live root Kernel context for typed non-Application
   child Features and checks **the complete parent Item / owning Invocation
   chain** before the durable Store retry CAS. Terminal ancestors return
   `harness.ErrConflict` without rotating the child's identity. The
   existing `withPhysicalInvocationDispatch` post-grant parent fence
   remains necessary and unchanged for concurrent parent termination.

   A positive test confirms that a child Application under a live Agent
   still retries normally, even though Application execution has **no**
   Kernel Run of its own. No generic Driver or alternate recovery state
   machine was added.

## Verification on exact implementation Head

| Gate | Evidence |
| --- | --- |
| `make check` | PASS, exit 0: Go source modules tests, race, lint (12 modules, 0 issues), formatting/tidy/vet, TS, smoke, clean source-cohort consumers |
| `make coverage` | PASS, Go statement coverage **73.65%** (floor 70%), TS statement coverage **88.08%** |
| `make security` | PASS: no reachable Go-code vulnerabilities, npm high-severity audit clean. Three required dependency-module vulnerabilities have no detected reachable call path |
| `make integration` | PASS: actual PostgreSQL 16/Redis 8, Kernel/Harness Store recovery, multi-process restart/cancellation. Docker containers cleaned |
| `make integration-otel` | PASS: real pinned Collector trace/metric E2E. Docker container cleaned |
| `go test -race -count=10` focused Harness Retry | PASS: missing frozen Actor/Thread, terminal root parent, terminal intermediate Invocation, valid Application child, and accepted retry replay |
| `git diff origin/main...HEAD --check` | PASS |

## Scope and remaining boundaries

- This is a pre-stable **hard cut** in retry admission behavior, not a
  new protocol or wire contract. Previously persisted roots without the
  required frozen Actor/Thread must be inspected, drained or quarantined
  before retry; there is no Session-derived compatibility reconstruction.
- Retry preflight is a read across Harness and Kernel authority, not a
  cross-Store atomic transaction. If a parent terminates **after** this
  check and a new child Attempt CAS commits, the existing durable physical
  dispatch grant and parent liveness check still prevent the child effect.
  We do not claim the newly accepted Attempt can always be rolled back,
  or that any physical external side effect executes exactly once.
- The `ExecutionClass` set remains closed with Feature-specific dispatch
  and recovery semantics. No generic Feature Driver, new Kernel status,
  SQL schema version, automatic retry/migration or remote Provider work.
- `v0.1.0-beta.24` remains an unreleased coordinated cohort; this is
  source-level acceptance only. Future merge requires independent review
  and GitHub required checks of its **actual final Head**. Published-module
  no-replace release checks and tags are separate prerequisites.
- Two unrelated local untracked OpenAI probe scripts were never staged,
  altered or removed.
