# Running-task inputs — scoped first delivery

Scope: optional root-Agent Host input log and applied cursor, plus the existing
independent-child regression. Part of PR #132; not a native ordinary-Tool
parallel executor, a front-end feature rollout or a new execution authority.

## Exact behavior

- A new Run freezes a source binding. Missing/changed binding prevents new model
  work; old Runs with no binding do not silently acquire one at restart.
- A read-only trusted Host log supplies contiguous, immutable, authenticated user
  records. The Kernel still persists the user messages and applied sequence in
  one original revision CAS. A queued sequence is not an execution receipt.
- Read points are before a new ModelInvocation and before an otherwise final
  answer. A pending frozen request is never reprojected; pending Tool calls and
  unknown effects must settle before a new model step can use input.
- If input arrives while the final model request is running, its old answer can
  become intermediate history and the new user message drives the next bounded
  step. No budget, model, approval, tool scope or repair allowance is enlarged.
- Already admitted tool batches are not interrupted. Late queue acceptance can
  race completion/cancellation; the Host must compare the final applied cursor
  and explicitly carry unused input into a new Turn.
- The Memory log is bounded and process-local. It partitions by original
  Run/Actor/Thread, enforces idempotent IDs and rejects a cursor beyond retained
  history. It is not presented as durable external-message delivery.
- The source is not inherited by delegated children. An input-enabled runner
  does not claim the no-Host-callback native cancellation ownership shortcut.

## Observed tests

The new regressions cover concurrent input during a physically blocked model
call; unchanged already-dispatched request; stale-final-answer continuation;
scoped/idempotent queueing; terminal submission refusal; lost Memory history;
hard model-limit preservation; unknown Tool outcome preservation; changed source
binding; Runner reconstruction without duplicate delivery; malformed/gapped,
duplicated and oversized batches; and the same behavior through bounded Harness
Context without rereading the Host transcript or creating another Turn.

The task gate also runs the existing three-child admission/restart/receipt test.
It proves existing independent child admission and receipt accounting, **not**
new ordinary-Tool physical concurrency. That implementation still needs its
own per-call multi-in-flight receipt/unknown-outcome/cancellation fault matrix.

## Validation at this delivery

- Full Agent, Model and Harness suites, plus all HTTP packages, passed with race
  detection after the source and projection changes.
- `make agent-quality-regression agent-baseline-test` passed; Python protocol
  suite remains **159 tests**. No live Provider run was launched.
- Full workspace Go lint and the clean external Go/TypeScript consumer gate
  passed, including new optional source-port and public helper contracts.
- A previous whole-workspace test/race attempt belongs to the earlier code head;
  it is not silently promoted into complete certification of this extension.
- Long-history allocation results and the correction to the initial retention
  hypothesis are in the separate projection audit.

Self-review only, not an independent audit. Final combined PR #131 heads and
required PR Head CI must be revalidated; earlier merge-tree evidence is pinned
to the exact older heads named in the first delivery audit.
