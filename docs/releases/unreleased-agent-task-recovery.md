# Unreleased upgrade note — single-Agent task recovery

This change targets the next immutable prerelease. It does not change released
module tags or persistence schemas.

## Added API

- `Runner.ResumeTurn(ctx, turnID)` resumes the existing direct-Agent root of a
  durable Harness Turn through Agent's current revision/claim/retry contract.
- `SessionClient.Resume(ctx, stableTurnID)` performs the Session identity check
  and resolves the same durable Turn before recovery.
- `OpenSession` accepts a nil `Transcript` for Load/Refresh/Resume handles.
  `Prompt` still requires a transcript and returns `ErrInvalidRequest` without it.

Resume does not create a replacement Run, alter an immutable pending model
request, retry a terminal task, approve a Tool, grant budget, or replay an
uncertain Tool effect. It restores persisted Context, advances only an admitted
running Agent, and projects the root result back into the Harness Turn.

If the root Invocation or Runtime Run does not yet exist, recovery reports that
missing fact. Retry the original Prompt with its original identity/input to
reconcile interrupted creation. Existing approval, external-wait, budget,
continuation and child-Feature APIs retain their established roles.

## Host integration

Use the same authenticated actor and tenant-scoped HostThread identity after a
restart. Reconstruct the same provider binding and registered Tool definitions;
Agent continues to reject invalid frozen requests or changed Tool authority.
Persist both Kernel Run state and Harness/Context state for process recovery.
Memory-only composition is an offline demonstration, not durable storage.

See the [single-Agent task guide](../agent-task-workflow.md) and the runnable
[task-agent example](../../go/agent-runtime-harness/examples/task-agent/README.md).
The quickstart now executes an Agent and Tool instead of only creating a Kernel
Run record. Core documentation also corrects its obsolete statement that a
non-fenced Resume could ignore durable model retry due times.

## Compatibility and evidence

This is an additive Go API change. No exported symbols are removed, no Store
ports are added, and no database or persisted-record migration is introduced.
No compatibility bridge or additional execution authority is introduced.
The standalone integration test module requests the patched Go 1.27.3 toolchain
when running with `GOWORK=off`, matching the contributor workspace baseline.

Validation covers deterministic task completion, Context artifact use, recovery
with no fresh transcript, ownership isolation, terminal/no-op behavior, retained
approval/budget/retry/uncertain-effect guards, clean Go consumer compilation, and
real PostgreSQL recovery. Offline model fixtures do not certify live-model task
quality or production exactly-once external effects.
