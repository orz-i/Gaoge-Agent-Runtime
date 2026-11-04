# Context Run ownership rehydration — independent P1 governance

## Baseline and scope

Base: `main@479533429b7b0fd3ce1289612b6550e3fbe43c21` (PR #87 merged).
Local branch: `governance/context-run-owner-20261104`.
This is a bounded correction in `go/agent-runtime-harness/context_run_middleware.go` and focused tests. No Kernel status, Store schema, new Context API, adapter, feature router or compatibility shim.

## Independently reproduced P1

A process-local Go `context.Context` may be reused when invoking a different physical Agent Run. Before this correction, `ContextRunMiddleware.hydrate` trusted `CurrentContextWindowBinding(ctx)` without comparing its Turn or access level with the Run being dispatched.

A read-only independent audit overlay on original `main` built two durable Agent Invocations in different Sessions, Turns and Context Checkpoints, then called `Run` for the second with the first's valid `owner` Context. Under `go test -race`, the second Run received the first Turn's `ContextCheckpoint` and `ContextWindowOwner` instead of its own. The same source also allowed a descendant Run to inherit the parent's `owner` access (rather than `read_only`) and an unresolved standalone Run to borrow a foreign checkpoint.

Both failures are **scope/privilege authorization violations**: a current physical RunID, not the process Context, must prove which Harness Turn owns the active Context. This is a potential cross-Session transcript exposure and unauthorized shared Context rollover.

## Bounded correction

- Before using a process-local binding or checkpoint, `ContextRunMiddleware` resolves the current Run through the durable `GetInvocationByExecutionRefID` and, when needed, its parent `RunRelation`, to the correct Harness Turn and maximum read/write access.
- A same-Turn binding can retain the existing optimized checkpoint rehydration only after that durable association is proved. Any explicitly narrower `read_only` binding remains read-only, and descendants may not inherit `owner` even if the reused Context says so.
- A Context bound to a foreign Turn is cleared and rehydrated from the actual Run's Turn. An unknown or empty RunID cannot justify borrowing process Context; no binding/checkpoint propagates. A Turn without a durable Context head also clears stale inherited Context.
- A Specialist created by Handoff is **still excluded**, and so are its nested Agent descendants reached through durable RunRelation ancestry. The full ancestor chain is checked before ordinary Turn resolution. A failed owner-index read is an error rather than evidence of an ordinary child; an inconsistent relation record cannot authorize the wrong parent. Even a newly created Go context for a Specialist descendant cannot borrow the Supervisor's transcript.
- Existing durable checkpoint identity, scope, revision and content-hash verification is preserved. No new persisted state or fallback; direct Agent callers without a Harness owner run with no Harness Context.

## RED → GREEN and regression scope

The permanent `context_run_owner_fence_test.go` test suite was RED on original code for wrong-Session context, descendant owner escalation and unresolved-run context leakage. A legitimate same-Run explicit `read_only` control passed before and after.

The first correction turned new tests GREEN but broke the existing independent Specialist isolation test (RED); the original exclusion was restored at the authoritative boundary and a new inherited-Supervisor-Context Handoff test was added. Subsequent focused Race runs and complete Harness module tests passed.

Additional cases prove that a bare Context checkpoint without a Turn binding cannot leak to an unresolved Run and is replaced by the current Run's durable checkpoint when verifiable, including after stale process checkpoint rollover. Existing root ownership, descendant read-only and Specialist restart regressions are retained.

## Follow-up independent P1/P2 audit and repair

The subsequent independent **read-only** audit revealed two gaps in the
first local repair:

1. **P1 — Handoff descendant isolation and transient read failure:**
   `hydrate` only inspected the requested Run for direct Handoff identity.
   A child of that Specialist, reachable by an immutable RunRelation,
   inherited the Supervisor Turn's Context at `read_only` privilege. This
   reproduced with **both an empty Go context and an inherited Supervisor
   binding**. Separately, injecting a one-time error on the first
   `GetInvocationByExecutionRefID` call caused the direct Handoff child
   to inherit Supervisor Context when a subsequent lookup succeeded.
2. **P2 — mismatched RunRelation authority:** `GetByChild(runID)`
   could return a record whose `ChildRunID` belonged to a different Run;
   the resolver trusted that record's `ParentRunID` and materialized the
   wrong Turn. This requires an inconsistent/custom relation source.

Three independent audit reproductions were copied into the permanent
`context_run_owner_fence_test.go` suite and confirmed **RED before
production changes**. The fix is limited to a Handoff-ancestor walk of the
existing immutable RunRelation path, propagating every unexpected Store
lookup error and validating the returned ChildRunID and exact parent
identity at the shared relation lookup boundary. The ordinary resolver
and Context/Agent/Kernel persistence contracts remain unchanged.

**Post-fix validation:** The three audit regressions are **GREEN under
`go test -race -count=5`**, as are the existing root, descendant,
read-only and direct Handoff isolation regressions. Additional permanent
cases guard blank/padded/self-referencing parents, wrong/empty child IDs
and cyclic ancestry. There is no new lifecycle state, shim or migration.

**Fresh post-P1/P2 repair full verification:**

- **PASS, exit 0:** complete repository `make check`, rerun on the final
  source *after* both ancestry and relation-index fixes. Includes all Go
  unit/Race/Vet/Lint modules, TypeScript lint/typecheck/**46 tests**/build,
  benchmark smoke, OTel/A2A and beta.24 local clean-consumer checks.
- **PASS, exit 0:** real PostgreSQL 16 / Redis 8 external-services
  integration on isolated Docker containers and dynamic loopback ports,
  including cross-connection cancellation, Run restart and Redis recovery;
  the temporary containers were removed.
- **PASS:** complete Harness module `go test -race -count=1 ./...`, focused
  Context ownership/Handoff/relations `go test -race -count=10`, Harness
  `go vet ./...` and `golangci-lint run ./...` (**0 issues**).
- **PASS:** `gofmt`, relative Markdown links, no trailing whitespace and
  `git diff --check`. At this previous gate checkpoint only six governance
  files had changed; the original user-owned OpenAI probes were excluded.

## Final independent TOCTOU and archived-Context follow-up

A subsequent read-only independent audit found two additional gaps:

1. **P1 — relation visibility TOCTOU:** The previous implementation
   independently traversed the Run ancestry in `hasIsolatedHandoffAncestor`
   and `resolveContextTurnForRun`. If a child's relation was not yet visible
   on the first lookup but became visible on the second, Handoff isolation
   was not rechecked. A child of an isolated Specialist received the
   Supervisor's `read_only` Context. Both fresh and inherited Go contexts
   reproduced this under Race. The test uses a relation source that returns
   NotFound once followed by the newly committed immutable relation.
2. **P2 — archived Context leak through a separately supplied read Tool:**
   The public `ContextArtifactToolHandler` used the ordinary Turn resolver,
   not the Handoff-exclusion proof. A direct Specialist and its descendant
   could read exact Supervisor archival payloads with a known Artifact ID.
   The default Handoff tool-assignment policy excludes `harness.*` tools,
   so this is a conditional public Handler/explicit Host tool-registration
   risk rather than a demonstrated default Handoff execution path.

The two independent test families were copied into the permanent
`context_run_owner_fence_test.go` and reproduced **RED** on the unchanged
candidate. The bounded correction adds one private
`resolveContextRunAuthority` traversal that validates every relation once,
checks every ancestor for Handoff ownership, and identifies the nearest
indexed Harness Turn and maximum permitted Context access. Both the Context
Run middleware and Context Artifact Handler use this proof. The Handler
rejects isolated Specialists before fetching any archived bytes, returning
`ErrConflict` even for an exact Artifact ID. Existing shared-budget/other
Feature Turn resolution remains unchanged. No new public API, schema,
Kernel/Agent execution state, migration or compatibility shim was added.

**RED → GREEN:** Both independent tests passed `go test -race -count=5`
after repair, including a strict assertion that the changing relation was
queried exactly once and that isolated artifact reads returned only
`ErrConflict` with no content. Pre-existing root, descendant, direct
Handoff, relation-identity and normal authorized Artifact tests also passed
Race ×5. A temporary single-pass proof was tested independently before the
production edit, with the whole Harness module unit suite passing.

**Fresh post-TOCTOU-and-archive verification:**

- **PASS, exit 0:** complete repository `make check` re-executed after the
  final Context Middleware and Artifact Handler source changes. This covers
  Go workspace unit tests, Race, Vet and Lint, TypeScript lint/typecheck/
  **46 tests**/build, benchmark smoke, OTel/A2A and the beta.24 local
  clean-consumer cohort.
- **PASS, exit 0:** real PostgreSQL 16 and Redis 8 external-services
  integration on isolated Docker containers with dynamic loopback ports,
  including cross-connection cancellation and continuation/restart recovery.
  Temporary services removed at test completion.
- **PASS:** complete Harness module `go test -race -count=1 ./...`,
  security/positive-path matrix `go test -race -count=10`, Harness
  `go vet ./...` and `golangci-lint run ./...` (**0 issues**).
- **PASS:** strict one-read relation count, denied archive bytes and
  `ErrConflict` assertions, Go formatting, local documentation links and
  `git diff --check`. Only seven intended governance files are in the
  final candidate; both pre-existing untracked OpenAI provider probes are
  excluded and their original SHA-256 hashes are preserved.

These are local implementation results, not a new-head GitHub CI result or
independent post-fix merge-admission approval. No commit, push, PR, schema
migration or beta.24 release is included.

## Last independent P2 audit — cached Checkpoint and Invocation integrity

A subsequent read-only independent audit found two more **P2 conditional
integrity** gaps in `context_run_middleware.go`:

1. `hydrateExistingBinding` reused process-local Context contents when
   the cached Checkpoint ID and `ContextCheckpointRef` metadata matched the
   durable Turn, but never verified that the *message bytes* still matched
   their `ContentHash`. An intentionally altered in-memory Checkpoint with
   unchanged metadata was accepted under both `owner` and `read_only`,
   replacing model-visible history despite a valid original Store record.
   The process-only setter is private; no external user-controlled mutation
   path was established by this test.
2. `resolveContextRunAuthority` accepted a persisted Invocation with a
   broken `InputHash` as ordinary ownership evidence. When the invalid
   record had once been an isolated Handoff, `isHandoffDelegationInvocation`
   correctly returned false but the generic resolver then exposed the
   Supervisor Checkpoint at `read_only`. The test injected an inconsistent
   Invocation on the read path; normal Store creation validates hashes.

Two independent negative regression tests were copied from the read-only
audit into permanent `context_run_owner_fence_test.go` and reproduced
**RED before the production patch**. The bounded fix requires
`validContextCheckpoint` and same-Session durable-reference validation
before accepting a cached Checkpoint; on mismatch, rehydrate the durable
Turn head. It also requires `validInvocation` before any Invocation can
lend Context ownership, rejecting corrupted records with `ErrConflict`
before the Run is dispatched. The second negative test asserts the
failure is classified and no downstream RunNext was called.

An isolated hypothetical patch passed both independent audit cases and
normal Root/descendant/Handoff/Artifact regressions under `go test -race
-count=5`, plus the Harness full module unit suite. The actual bounded
patch then passed the same new negative tests RED→GREEN and existing
security regressions under Race. No public API, persisted schema, new
lifecycle state, compatibility shim or extra Context ownership resolver
was introduced.

**Fresh final-source verification after both integrity corrections:**

- **PASS, exit 0:** complete repository `make check`, run after the
  Checkpoint and Invocation integrity edits, including Go unit/Race/Vet/
  Lint, TypeScript lint/typecheck/**46 tests**/build, Benchmark Smoke,
  OTel, A2A and beta.24 local clean-consumer checks.
- **PASS, exit 0:** isolated real PostgreSQL 16 / Redis 8
  `node scripts/run-integration.mjs --external-services`, including
  cross-connection cancellation, restart/continuation and Redis recovery.
  Temporary Docker services were removed.
- **PASS:** focused Context/Handoff/Artifact and integrity matrix under
  `go test -race -count=10`; entire Harness module under
  `go test -race -count=1 ./...`; Harness `go vet ./...` and
  `golangci-lint run ./...` (0 issues). Existing root, read-only child,
  specialist descendant, TOCTOU and archive-read regressions stayed green.
- **PASS:** `gofmt`, local Markdown links/whitespace, `git diff --check`
  and exact seven-file governance change scope. The two original
  untracked OpenAI probe scripts remain untouched, excluded and hash-
  verified; unrelated Git worktrees remain unchanged.

These receipts replace the prior historical full-gate claims for this
last repaired source. This is a **local implementation validation only**;
it is not an independent post-fix merge-admission audit or GitHub PR CI.
No commits, pushes, PR creation, schema migration or beta.24 release.

## Independent read-boundary follow-up — three reproduced integrity gaps

A fresh **read-only audit of the previous repaired source** used temporary
Go overlays (outside the worktree) to reproduce three conditional gaps:

1. **P1 — incomplete Handoff Input:** `validInvocation` previously accepted
   a missing `Input` with a leftover nonempty `InputHash`; the Specialist
   was no longer recognized as isolated and was given Supervisor `read_only`
   Context. Both missing payload/retained hash and missing payload/erased
   hash must now fail closed for a child Agent Invocation.
2. **P2 — corrupted durable Checkpoint:** `hydrateTurn` checked scope, ID and
   reference fields but did not recompute `ContentHash` on the Store result.
   A modified transcript with the original reference was delivered downstream.
3. **P2 — incomplete RunRelation read validation:** a relation that the
   writer's `runrelation.Prepare` rejects (unknown Kind, missing OwnerNodeID
   or CreatedAt), or would normalize (noncanonical Kind/OwnerNodeID), could
   still authorize a foreign Run's inherited Supervisor Context.

All three were first reproduced **RED under `go test -race`** before production
edits. The bounded fix strengthens `validInvocation`, validates the complete
reloaded Checkpoint before dispatch, and applies the existing RunRelation
writer contract at the read-side authority boundary. Existing test fixtures
for external-item roots, late cancellation children and SQL root-invocation
conformance were made internally consistent without weakening their original
behavioral assertions. Permanent regressions include the three independent tests and
subcases for both missing-input variants and distinct relation fields.

**Fresh final-source receipts after all three read-boundary fixes and
fixture corrections:**

- **PASS, exit 0:** full repository `make check`: Go unit/Race/Vet/Lint,
  TypeScript lint/typecheck/**46 tests**/build, evaluation and benchmark
  smoke, OTel/A2A product checks and beta.24 local clean-consumer gate.
- **PASS, exit 0:** isolated PostgreSQL 16 / Redis 8 external-services
  integration with dynamic localhost ports, including cancellation,
  cross-connection recovery, scheduler restart and Redis terminal fencing.
  The temporary Docker containers were removed.
- **PASS:** permanent independent read-boundary regressions under
  `go test -race -count=10`; complete Harness module `go test -count=1 ./...`;
  root-invocation conformance (Memory and PostgreSQL adapters), cancellation
  admission and external-item fixtures. Local gofmt, doc links and
  `git diff --check` passed.
- The two unrelated untracked OpenAI probe scripts were hash-verified and
  left untouched. No commit, push, PR, migration or release was performed.
  This is a **local repair verification**, not a new-head GitHub CI result
  or independent post-repair merge-admission audit.

## Invariant-driven follow-up and bounded ownership-convergence repair

An additional **read-only independent audit** challenged the preceding
repair with an `invariant × operation × abnormal Store result` matrix. Its
single-run negative probes reproduced the following previously untested
conditional failures:

1. A `GetTurn` result with a different Turn ID rehydrated a foreign Session's
   valid Checkpoint under Start, Resume and approval-resume.
2. `GetContextArtifact` could return a different same-Session Artifact, or
   altered payload with the old checksum, without being rejected by the
   Handler's scope-only guard.
3. `WithoutContextWindow` could be followed by an internal in-process
   rebinding; a middleware early return forwarded the forbidden checkpoint.
4. An indexed root Invocation could lose its frozen input/hash, change its
   Invocation ID, or change its CapabilityKey, and still be treated as a
   valid `owner` when its canonical record did not match.
5. The previously separate Budget resolver accepted a forged Invocation
   TurnID and associated the original physical Run with a foreign Turn's
   shared-budget ledger.

Because the same family recurred across distinct consumers, the repair
**converges rather than adds another independent authorizer**:

- `resolveContextRunAuthority` is the single Run ancestry / durable Turn
  resolver for Context, Artifact and shared-budget consumers. Context's
  legacy duplicate walker was reduced to a forwarding wrapper. It examines
  each relation once and preserves Handoff isolation while allowing its
  legitimate Supervisor shared-budget accounting.
- An indexed Invocation is corroborated against `GetInvocation(ID)` using
  the existing immutable identity comparison. Mutable status/revision may
  change concurrently without silently transferring ownership. The exact
  returned Turn ID and existing Turn validity rules are checked.
- Artifact reads now validate exact requested ID and `ValidArtifact`, not
  merely matching Session Scope; Context reads and hydration honor sticky
  `WithoutContextWindow` exclusion.
- The independently written 126-state transition product, 1,000 seeded
  generated cases and previous negative probes are checked into
  `context_authority_matrix_test.go`, not left only in temporary overlays.
  All previously RED negative probes became GREEN. Focused matrix Race ×3,
  complete Harness module Race and cross-boundary Provider identity/replay
  tests passed locally.

The matrix injects inconsistent Store values; these are not proof that a
normal PostgreSQL write path generates those records or that a remote
actor can create them. The authority boundary is still a trusted Store;
its canonical read is the source of truth and must enforce its own durable
integrity. No DB schema, Kernel/Agent state, public API or compatibility shim
is introduced.

**Fresh implementation receipts on the converged source:** complete
repository `make check` **PASS (exit 0)**, including Go Unit/Race/Vet/Lint,
TypeScript lint/typecheck/46 tests/build, benchmark smoke, OTel/A2A and
beta.24 clean-consumer release check. Isolated PostgreSQL 16 / Redis 8
external-services integration **PASS (exit 0)** with temporary Docker
containers removed. The permanent matrix passes `go test -race -count=3`
and the entire Harness module passes `go test -race -count=1 ./...`.

**Independent review limitation:** a separately launched read-only Codex
review could not authenticate (HTTP 401); an independent local Pi CLI model
was not available. Do **not** interpret the implementer's passing tests
or prior audit as a completed independent post-fix review. A final
independent source/PR-head audit and GitHub branch-protection checks on the
exact committed PR Head remain blocking merge gates. These local receipts
supersede the previous version's test evidence; no merge authorization is
claimed here.

## Earlier verification and exclusions

- Permanent targeted tests: `go test -race -count=10` over affected Context Run, descendant and Handoff cases — PASS.
- Full Harness module `go test -count=1 ./...` — PASS.
- The independent old-source overlay from `main@4795334` made all five relevant owner/binding/descendant/Handoff test groups **RED** under Race; the actual governance source passed the same tests at `go test -race -count=5`.
- **PASS, exit 0:** complete repository `make check` after the production fix, covering all Go unit, Race, Vet and Lint, TypeScript lint/typecheck/46 tests/build, OTel, A2A, benchmark smoke and beta.24 local clean-consumer checks.
- **PASS, exit 0:** real PostgreSQL 16 / Redis 8 `node scripts/run-integration.mjs --external-services`, isolated local Docker containers and dynamically assigned loopback ports, including continuation, cancellation and restart recovery. Temporary containers removed.
- **PASS:** Harness `golangci-lint run ./...` (0 issues), Harness Vet, focused security Race ×10, full Harness unit tests, Go formatting, document links and `git diff --check`.
- The original two untracked OpenAI probe scripts and unrelated worktrees are preserved with no modifications. No commit/push/PR or release operation in this local governance slice.
