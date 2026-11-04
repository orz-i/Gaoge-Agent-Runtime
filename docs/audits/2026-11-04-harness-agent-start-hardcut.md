# Harness / Agent top-level startup minimalization — hard-cut governance

## Baseline and scope

- Base: `main@fb84fc1e56612bf912d8e252b1cd1b8d2625f45a` (PR #88 merged).
- Working branch: `governance/harness-agent-minimal-hardcut-20261104`.
- Scope: **one top-level Turn admission, one Agent physical-start construction, one first-admission top-level result settlement**. There is no change to Host intent selection, Provider binding, Agent's Model/Tool loop, Kernel lifecycle, or persisted schema.
- This is a **private implementation hard cut**. Removed private entry names have **no forwarding aliases or compatibility bridges**. Public `StartAgentTurn`, `StartTeamTurn`, `StartGroupChatTurn`, `StartWorkflowTurn`, `StartApplicationTurn`, `PrepareRun`, `ModelInvocation` and `SessionClient` contracts remain intact. This does not claim to eliminate the distinct Agent replay/recovery semantics.

## Evidence of real duplication

1. `StartAgentTurn` independently executed `normalizeStartRequest`, `resolveConfigExecutionPolicy`, `sealStartAdmission`, `SealConfigSnapshot`, and `persistStartEnvelope`, although top-level composite Features already performed those exact operations via `prepareTopLevelFeatureEnvelope`. The common private helper is now `prepareTopLevelStartEnvelope` and returns one sealed `topLevelStartEnvelope` used by all top-level Feature classes. The old helper and type are removed; no alternate admission path is retained.
2. The initial Agent Start and the admitted-but-not-yet-created Agent Run recovery each built a separate `agent.StartRequest` and physical grant, with duplicated Model Options, Tool Keys, allowances and RequestID fallback. Both now use the single private `startAgentPhysical` function, retaining the **same original `Invocation.TurnID`**, sealed request and immutable config. The distinct pre-Run recovery decision remains separate from first creation.
3. Agent first-admission and Team/GroupChat/Workflow first-admission had duplicate no-snapshot/unknown-effect/no-effect handling and result synchronization. Their private settlement is now `finishTopLevelStart` without a compatibility alias. **Only `ExecutionAgent` retains the existing cancelled-Turn + stale Kernel `ErrConflict` normalization**. Composite Features retain their own pending-error normalization. Recovered Agent starts still use their own replay-specific settlement, rather than being inadvertently turned into a new root admission.

## Safety invariants retained

- **Authority:** Host explicitly chooses one `ExecutionClass`; the sealed `AdmissionHash`, original root Invocation and frozen identity prohibit reclassifying a Turn on replay.
- **Effects:** every physical Start still goes through `withPhysicalInvocationDispatch` and durable grant/receipt validation. An outcome-unknown physical Start must remain unresolved, not be marked failed or reexecuted blindly. A failed root may be projected only with an original certified no-effect receipt.
- **Idempotency:** exact replay does not issue a second Model call, change Tool identity, widen allowances or rebuild a pending `ModelInvocation`.
- **Recovery:** a failed presentation Item append before physical Start can be repaired using the same Turn/Invocation. A completed or cancelled root does not restart, while native cancellation-vs-Start conflict converges on the terminal Turn as before.
- **Layering:** Kernel/Store state, Provider replay and Tool result/receipt protocols are unchanged; no new owner or status is persisted.

## Focused regression and limits

- New `TestHardcutTopLevelEnvelopeCannotSwitchAgentToTeam` proves the sealed Agent Turn cannot become a Team Turn and exact replay triggers no second model invocation.
- New `TestHardcutAgentPhysicalStartEqualsPreRunRecovery` compares the real frozen model request in initial and pre-Run-crash paths, verifies immutable Model/Options, rejects changed Goal on replay and permits only one physical Model call.
- Existing cases retained for uncertain native Start, exact no-effect receipts, concurrent cancellation, root-origin checks, Team/Workflow start/replay and Session examples.
- All relevant unit/race checks must run on the final commit; full repository checks and real-service integrations must be recorded separately. This refactor does not certify Hosted deployment, third-party Provider or mobile runtime behavior.

## Local verification on the final source cohort

- **PASS:** Harness `go test ./... -count=1` and Harness `go test -race ./... -count=1`; the focused newly introduced physical-start request equality test also passes with `-race`.
- **PASS:** Core (`go/agent-runtime`) and all remaining Go module `go test -race ./... -count=1` runs, completed in separate short batches across the repository's twelve test modules. No reported races.
- **PASS:** `make go-lint ts-lint ts-typecheck ts-test ts-build` (Go lint zero issues, TypeScript 46 tests) and `make eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` (clean consumers for the beta.24 local cohort).
- **PASS:** `make metadata agent-baseline-test fmt-check tidy-check go-vet` (Python baseline 59 tests); the initial full `make check` ran successfully through `go-test` and entered `go-race` before the remote command connection ended. The remaining targets were run separately and completed successfully. Consequently **a single uninterrupted `make check` exit code was not obtained**, despite coverage of its individual targets.
- **NOT RUN:** actual GitHub PR-head CI, live Provider calls, device/production acceptance, and external PostgreSQL/Redis integration. These are separate from this local architecture refactor's pass criteria.

## Exit criteria

No old private helper remains; no parallel execution pathway or backwards-compatibility forwarding function is introduced; frozen Turn/Invocation, native Tool/Model and Store checks pass; review final diff for weakened grants/no-effect/cancellation logic. Preserve user-owned untracked Provider probe scripts. Do not push, create PR or merge without a separate request.
