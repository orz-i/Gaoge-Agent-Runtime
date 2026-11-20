# Stage 3: explicit Host WorkingState

## Scope

Implemented on `codex/agent-session-output-limits`, starting at Stage 0/1 commit
`5c34b2f`, in the existing dedicated worktree. This stage supplies explicit Host
task data; it does not implement Stage 2 diagnostics or Stage 4 automatic
summarization. No new Store interface, database schema, model call, approval
path or execution authority is introduced. This record describes validation
before the Stage 3 implementation/review commit.

## Implementation

- `go/agent-runtime/context/working_state.go` defines bounded state and its
  exact checkpoint provenance, cloning, validation and user-role projection.
  `Window.WorkingState` is independent of transcript `Entries`.
- `context/window.go` validates updates against the selected previous
  checkpoint, advances generation/cache identity on replacement or reset,
  carries compatible state without appending it, and preserves it through
  Capture, compaction, rollover and model-window changes. Existing raw
  Artifacts retain their original scoped lookup contract.
- Harness `ContextSeed` freezes the optional state in admission. New updates
  are preflighted before persisting a Turn. The review additionally freezes
  the Context Open base, including a known absence of state/checkpoint, in
  optional sealed Config metadata. An admitted retry restores that original
  plan. Other Turns and pending model intents remain immutable. The original
  expected head is frozen too; the existing Store commit keeps a recovered
  older Turn detached if another Turn has since advanced the head.
- `SessionClient.ResolveContextSourceBoundaryForPath` lets a Host resolve the
  actual source-aligned base through the existing Session handle. It checks a
  frozen Turn owner before and after the read and validates the Store result's
  Session scope and static fingerprint. Explicit source-delta lookup also
  checks that the returned checkpoint ID equals the requested ID.
- `docs/working-state.md` describes Host use and retry behavior. The existing
  standalone Harness consumer compiles the new public types and method without
  changing any required persistence-port methods.

## Bounds, trust and compatibility

Version is `1..2147483647` and must increase relative to the selected base
state. This bound also avoids integer ambiguity in the existing canonical JSON
hash path. State JSON is at most 16 KiB; each list has at most 32 entries and
each text value has at most 2 KiB of UTF-8 bytes. Source checkpoint ID and hashes
must be canonical; the covered source ID is separately bounded to 2 KiB.

All four source fields must match the selected base. Full-path new admission
cannot pin an arbitrary historical/runtime-tail checkpoint. A source delta
continues to use its explicit, verified base. A compatible ancestor's state
can apply to a descendant; an active sibling's state cannot cross branches.
The current user prompt remains present. WorkingState is inert user-role data
and cannot authorize Tools, approvals, budgets or larger output limits.

Nil state is omitted from Window and ContextSeed JSON. The legacy Window hash
fixture remains
`f24a47851b9adee648d4f1a0f31e8a858c7b360bad7f3a44d01eaf8ab7eaadfe`.
Old data remains readable; older executables must be upgraded before consuming
the new optional Config metadata or checkpoints that contain state. Evidence
references remain Host declarations; source hashing does not certify the truth
of a summary. See the [upgrade note](../releases/unreleased-working-state.md)
for the old Accepted-Turn compatibility limit and deployment sequencing.

## Validation

All commands below use Go 1.27.4 on Windows/amd64. Standalone consumers and
cross-process fixtures use `GOWORK=off`.

- Core module: `go test ./... -count=1` passed, including Memory conformance.
- Core Context: final `go test -race ./context -count=1` passed.
- Harness: `go test -race ./... -count=1` passed.
- Final Harness WorkingState focused race tests passed, including branch
  round-trip isolation, pre-admission rejection and interrupted checkpoint
  creation followed by head advancement and replay of the original seed.
- Core Context and Harness `golangci-lint` passed with zero issues.
- Original isolated Agent and Harness consumer gates passed, including the
  extended public WorkingState contract.
- Real PostgreSQL: original explicit composition, original OpenAgentSession,
  and WorkingState cross-process crash/recovery fixtures passed with `-race`.
  The state fixture preserves the exact pending request hash, original state
  and provenance, one user-role projection per call, archived evidence and the
  latest Turn checkpoint after recovery. Replacing the Host's unadmitted input
  does not replace frozen Context or a pending intent.
- Changed Go files pass `gofmt`; `git diff --check` passes. The temporary
  PostgreSQL container was stopped after verification.

The same invoice oracle reports:

| Invoice fixture | Invoice model calls | Business effects | Invoice request JSON bytes per call | Invoice total bytes |
| --- | ---: | ---: | --- | ---: |
| Explicit composition, nil state | 3 | 1 | 2,129 / 2,517 / 3,197 | 7,843 |
| OpenAgentSession, nil state | 3 | 1 | 2,129 / 2,517 / 3,197 | 7,843 |
| OpenAgentSession, explicit state | 3 | 1 | 3,160 / 3,548 / 4,228 | 10,936 |

The state case first creates a source checkpoint using a separate warmup Turn
and one fixture-model generation. Its complete test therefore has four model
generations; the table and request-byte totals cover only the three invoice
calls and exclude that warmup request. WorkingState itself invokes no model.

All retain the same exact-cent constraint, archived shipping evidence and
invoice result. These are serialized JSON bytes, not token counts or a quality
benchmark. Explicit state adds input in this fixture. Automatic compression
and model-quality evaluation are outside this stage.

The [Stage 0/1 Windows SQLite baseline failure](2026-11-20-agent-session-stage01-review.md)
remains documented: baseline and worktree both failed directory `Sync()` with
`Access is denied` in the pinned Windows environment. This stage changes no
SQLite source or durability behavior and does not claim the full Windows
SQLite integration/release gate passed. No push, merge or later stage was
performed; the original main checkout remains clean at `19151c9`.
