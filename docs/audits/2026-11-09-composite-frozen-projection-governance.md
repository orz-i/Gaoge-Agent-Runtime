# 2026-11-09 — Native composite Feature frozen-input projection convergence

## Decision and scope

This is a bounded **Agent / Harness complexity-reduction** slice on
`main@61b6df79f5b8fd6bc7327323258dbd130f5b90a0` (merged PR #99).
No second Agent model/Tool loop, general FeatureDriver interface, Host
intent router, Kernel state, SQL migration or Provider protocol is added.

The existing Harness native composite entrypoints Team, GroupChat and Workflow
project the original typed `Invocation.Input` into concrete physical
`StartRequest` values. Previously their top-level, child and retry paths
each rebuilt overlapping request fields independently. GroupChat additionally
built both a speculative pre-commit `runtimeRequest` and a separate
durable `groupChatInvocationInput`, then manually overwrote the former
with the latter. These parallel field maps can silently drift as Feature
fields evolve, including fields that matter to resumed external execution.

This change keeps each Feature's **one private, concrete** projection:
`teamInvocationInput.startRequest`,
`groupChatInvocationInput.startRequest`, and
`workflowInvocationInput.startRequest`. First root Start, child Start (where
supported), and durable retry now use the same projector. GroupChat
materializes **only the durable input** before admission. Physical execution
continues to receive the correct Run ID, original Actor/Thread and
attempt-specific RequestID from its established caller's execution proof.

## Safety invariants and trust boundaries

| Invariant | Path | Regression evidence |
| --- | --- | --- |
| Physical arguments derive from frozen source | Root Start, native child Start, retry | Team/Workflow existing integration tests and new all-three pure projection tests |
| Original execution identity never comes from a mutable Role/catalog | Original root vs child vs retried Run | Constructor tests supply a deliberately foreign Actor/Thread inside input and verify that the explicit frozen-execution owner wins |
| GroupChat policy cannot drift between original and retry | Directed, Selector, Handoff | Real Harness/Kernel/Store test captures the first and second Start, strips the intentionally different Run/Request IDs and compares all policy fields |
| Caller mutation after Invocation commit cannot change the original feature execution | First physical GroupChat Start, retry | Store fault fixture mutates host Goal, participant, role, Tool keys, selector options and model after `CreateInvocation`; both Starts use original frozen data |
| Invalid policy cannot perform effects | Selector missing model, Handoff with one speaker, unknown policy | New negative tests verify `ErrInvalidRequest` and zero physical feature calls |
| Original replay/unknown-outcome safety preserved | Existing Team/Workflow/GroupChat Run and lifecycle | Full Harness unit/race tests, existing protective cancel, effect-claim and unknown-outcome tests |
| Model quality/comparability remains versioned | Host quality-suite calibration | Unchanged 40/40 deterministic scripted oracle, 160 fake Model/120 fake Tool calls and unchanged dataset hash |

The closed `ExecutionClass` variants, durable `Turn`/`Invocation`/
`Attempt` IDs, original input bytes and hashes, Kernel CAS and lease/epoch
gates, RunRelation, completion and cancellation receipt checks are untouched.
Feature adapters still own their original Start/Resume semantics. No
new status, compatibility bridge, fallback authority, external API or
persistence layout has been introduced.

## Complexity and performance evidence

- Populated native composite `StartRequest` projection sites in
  `feature_invocation.go`: **8 before, 3 after** (one each for Team,
  GroupChat and Workflow). There were also three GroupChat zero-value
  `StartRequest{}` error returns, now eliminated. Thus all literal
  appearances, including zero-value returns, fall **11 to 3**. These
  methods are *private, typed, pure projections*, not a generic Feature
  runtime.
- The former GroupChat pre-commit `runtimeRequest` has been eliminated;
  there is one canonical frozen input rather than two mutable maps.
- This is an architecture/drift reduction, **not a measured latency or
  allocation optimization**. Adding small pure Go functions does not prove
  production speedups; perform workload-matched, repeated benchmarks
  before asserting throughput improvement.
- Full Harness `go test ./... -count=1`: PASS.
- Full Harness `go test . -race -count=1`: PASS.
- Quality scripted 40/40; unchanged scripted dataset
  `sha256:9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`.

## Exit criteria and remaining work

1. Entire SDK and Host integration `make check` must pass on the final
   candidate worktree with new tests and documentation included.
2. Portable `make benchmark-smoke` and offline Agent quality should pass;
   any real performance claim still requires repeated same-machine/same-fixture
   comparison. Original Provider and Token Counting quality baselines stay
   immutable.
3. An **independent post-change audit** must inspect the entire affected
   Team/GroupChat/Workflow Start/Child/Retry and later reconciliation chain
   (especially Actor/Thread, physical RequestID and replay behavior), not
   merely the diff.
4. No real paid Provider calls, new PostgreSQL/Redis services, GitHub PR Head
   CI or release is claimed by these local tests. This is a candidate
   governance slice, not an automatic merge approval.

Further reduction of Harness cancellation, interaction and feature executor
complexity is deferred until a separate fault-matrix audit demonstrates
that deleting a branch does not erase a legitimate unknown-effect or
same-Run recovery state.

## Implementation gate result (local, before independent audit)

- Full repository `make check`: **PASS, exit 0**; the ignored local receipt
  and log are `coverage/harness-frozen-projection/check.exit` and
  `coverage/harness-frozen-projection/check.log`. Includes Go workspace
  fmt/tidy/vet/unit/race/lint; Python Agent baseline; TypeScript
  lint/typecheck/test/build; evaluation, benchmark and OTel smoke; A2A
  product check and beta.24 clean-consumer installation/build.
- `go/agent-runtime-harness` full package tests and independent package Race:
  **PASS**. New tests cover each composite's original/frozen policy and
  explicit execution identity; GroupChat's three strategies, post-commit
  mutation, failed first attempt and same original policy on retry; three
  invalid-policy/no-physical-effect scenarios.
- `make agent-baseline-test`: **131 tests PASS**.
- `make benchmark-smoke`: **PASS** (one iteration per baseline case, not a
  same-fixture latency improvement claim).
- `make agent-quality-calibrate`: **40/40 scripted PASS**; original dataset
  hash, 160 model and 120 Tool synthetic invocation counts unchanged.
- Only the dedicated governance worktree has tracked modifications. The
  original main worktree, its owner-owned untracked probe scripts, historical
  paid reports and `main` Head remain unchanged.

This evidence closes the **implementation** stage only. A distinct reviewer
must still audit the full candidate before commit/push/PR admission.
