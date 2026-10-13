# External Tool Wait · Generation-aware CAS Race Remediation

Date: 2026-10-13. Status: accepted remediation scope, SDK follow-up to published beta.21; review blocker for Gaoge PR #32.

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
