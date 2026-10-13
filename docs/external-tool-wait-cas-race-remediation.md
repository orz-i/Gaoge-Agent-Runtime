# External Tool Wait · Generation-aware CAS Race Remediation

Date: 2026-10-13. Status: SDK source fix locally verified; **not yet released or adopted by Gaoge**. This is a follow-up to published beta.21 and addresses the SDK-side review blocker for Gaoge PR #32.

## Incident and failure invariant

A durable `tool_wait_deadline` or `tool_result_ready` continuation has a stable `WaitGeneration` independently of the Run revision. In beta.21, `Dispatcher.Dispatch` loads revision *N*, then calls `Agent.ResumeToolWait(N, generation)`. A legal concurrent change between those operations advances the Run to *N+1* without changing the waiting generation. `ResumeToolWait` correctly reports `kernel.ErrConflict`, but `Dispatch` classified every CAS conflict as a successful no-op. The continuation Worker subsequently ACKed the sole durable delivery while the Run remained waiting; the deadline or ready result could be lost permanently.

The original regression was reproduced deterministically with a real Agent Runner, memory Kernel and one injected revision change between the Dispatcher load and resumer call. This is a correctness defect, not a deployment-only test omission. Existing tests exercise revision advances **before** dispatch but not that interleaving **during** dispatch.

## Fix contract

- Preserve the existing typed SDK port, the immutable wait generation, and the Agent-owned `ResumeToolWait` CAS guard. Do not mutate release tags, persist a second state machine, or add a poll loop.
- For **generation-aware** continuations only, retry a bounded number of fresh Run reads and CAS-resumptions when the resumer returns `kernel.ErrConflict`. Rechecking the live Run and generation on every attempt means an obsolete/consumed wait remains a no-op.
- If contention continues beyond the local retry budget, return `kernel.ErrConflict` to the existing leased continuation Worker so it **NACKs** and retries via its bounded durable queue policy; never convert unresolved contention into ACK/success. Terminal/missing Run and consumed generation still ACK without re-execution.
- Preserve existing semantics for **non-generation** triggers (including child-terminal reconciliation and normal scheduled retry). A successful resumption must not be executed again by this delivery.
- `context` cancellation, genuine storage failures and unsupported Run kinds propagate to the Worker; no new server-side execution behavior, authorization, schema or migration is introduced.

## Deterministic acceptance

1. Deadline wait: a concurrent unrelated revision *inside* Dispatch must still produce `agent.tool_deadline_exceeded` and a terminal Run; the result is not merely an ACKed running state.
2. Result-ready wait: identical interleaving must resume the stored receipt exactly once, count one completed ToolCall and not invoke the model twice.
3. Persistent CAS contention beyond the local retry budget must return `kernel.ErrConflict`, not ACK. A later retry with unchanged waiting generation must complete the task.
4. Stale generation, cancelled/terminal Run and unrelated non-generation paths remain harmless/idempotent.
5. Re-run SDK Agent + Continuation + queue/harness integration and release governance gates. Real PostgreSQL/Redis/release checks remain required before publishing a new Beta; tests without live services must not be called live-service verification.

## Release and host ownership

beta.21 is already published and remains immutable. Prepare this source fix on an independent SDK branch with atomic documentation, regression and implementation commits. Only after a new SDK cohort is formally released may Gaoge PR #32 advance its SDK gitlink, nine Go modules, TypeScript package, workspace lockfile and governance pins together. No GitHub Quality CI is to be introduced into Gaoge. Do not push, merge or publish without separate authorization.

## Local acceptance evidence (2026-10-13)

Baseline `origin/main@3992311`, candidate branch `fix/external-wait-generation-cas-retry@66e8a6b` prior to this evidence-only documentation commit.

- The new deterministic red tests first reproduced the beta.21 defect: the deadline and result-ready jobs were wrongly ACKed while the Run stayed `running`; persistent conflicts also returned `nil`. With the fix, all regressions pass, including stale/terminal generations and a real in-memory leased Worker NACK -> redelivery -> ACK with exactly two claims.
- `pnpm install --frozen-lockfile --prefer-offline`: passed from the locked local content-addressable store.
- `go test -race -count=25 ./go/agent-runtime/continuation -run 'TestExternalWait.*CASConflict|TestExternalResultReadySurvivesCASConflictDuringDispatch|TestExternalWaitWrongGenerationDoesNotResume'`: passed.
- `make SHELL=sh check`: passed (nine Go modules' test/race/vet/lint/format/tidy, TS lint/types/tests/build, evaluation, benchmarks, OTel smoke, protocol/release checks and clean consumers).
- `make SHELL=sh coverage security integration integration-otel`: passed. Go statement coverage **72.70%** (floor 70%); TS statements **79.16%**. All nine `govulncheck` scans found no reachable vulnerabilities; the scanner reported one required-module vulnerability not reached by code, and `pnpm audit --audit-level high` reported no known vulnerabilities.
- Actual Docker-backed PostgreSQL 16 and Redis 8 suites: **23 real-service cases passed, zero skips** under `-race`. Collector 0.149.0 OTLP trace/metric gate passed, digest `sha256:0fba96233274f6d665ac8831ad99dfe6479a9a20459f6e2719c0d20945773b46`; isolated Compose projects were removed afterwards.
- Gaoge current `master@9df6ab5a` + PR #32 head `74228c98` + this SDK candidate source: isolated synthetic-merge `go test ./backend/internal/...` passed. This is a local committed-tree compatibility test, not a published SDK cohort or production deployment E2E.

These gates validate the local SDK source repair. No release, host SDK pin update, PR push/merge, or production browser execution acceptance is implied.
