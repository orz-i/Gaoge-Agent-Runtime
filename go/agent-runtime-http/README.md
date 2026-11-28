# Agent Runtime HTTP Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http` exposes HTTP v1 for Runtime
Runs, durable Events and Run Feed. The first-party `harness` submodule exposes the
Harness Turn snapshot, semantic feed and approval continuation resources on
the same versioned wire surface without making Harness part of Runtime Core.
The host injects `PrincipalResolver`, request metadata, the authoritative Run
loader, and an optional object-level `RunAuthorizer` before registering routes.
Passing a nil authorizer selects the fail-closed owner-only policy:

```go
shared := runtimehttp.NewShared(principalResolver, metadataResolver, runtime, runAuthorizer)
handler := runtimehttp.NewHandler(runtimehttp.Dependencies{
    Runtime: runtime,
    Feed: runFeed,
    Shared: shared,
})
runtimehttp.NewModule(handler).RegisterRoutes(apiV1)
```

Object authorization stays at the Host/HTTP edge. Explicit policy denials are
rendered as not-found so a caller cannot probe another tenant's Run IDs.

Handlers using `BindStrictJSON` accept one complete JSON document up to 8 MiB
(8388608 bytes), reject unknown fields and trailing JSON, and enforce `binding`
field rules (including nested DTOs) before invoking runtime operations. Invalid
JSON or fields return HTTP 400; oversized bodies return HTTP 413 with
`request.body_too_large`. Both errors leave runtime state unchanged.
The only first-party orchestration routes are Agent Run and Harness/Subtask
endpoints; no obsolete composite routes are mounted.

Start, read and cancel return the same snapshot, including the canonical
`agent.PendingToolView` from `agent.ViewState`; there is no projector injection
or separate cancel response. `GET /runs/:run_id` returns `eventHead`, not the full
Kernel Event journal. Use `GET /runs/:run_id/events?afterSeq=...&limit=...` for
bounded durable audit/event history. `/runs/:run_id/feed` remains the separate
semantic Run Feed and is not used as a compatibility substitute for the journal.
Feed sequences come only from retained events. A terminal, drained feed returns
204; HTTP never invents a terminal sequence or publishes a cancellation event.

Inject the existing owner `RunCanceller` through `Dependencies.Canceller` for
remote Runs. There is no Run-kind cancellation table: both A2A and ACP use
`agent.remote`, and only the original executor can verify and cancel its effect.
Without that owner, remote cancellation fails closed instead of cancelling a
local shadow. Native Runs may use the default Kernel canceller.

The unique wire contract is [contracts/agent-runtime/v1/openapi.yaml](../../contracts/agent-runtime/v1/openapi.yaml),
including the `harness` capability fragment and explicit cursor-expired
recovery headers for both Run Feed and Harness Turn Feed.

Run kinds include `agent`, protocol-neutral `agent.remote` and host extensions.
Generate/check the edge TS types from this contract with
`go run ./go/agent-runtime-http/internal/contractgen [-check]` at the repository root.
HTTP `v1` names the protocol namespace; the SDK still follows the
[Beta integration and upgrade contract](../../docs/integration-contract.md).
