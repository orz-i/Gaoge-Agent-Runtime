# Agent Runtime HTTP Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http` exposes HTTP v1 for Runtime
Runs and Workbench resources. The first-party `harness` submodule exposes the
Harness Turn snapshot, semantic feed and approval continuation resources on
the same versioned wire surface without making Harness part of Runtime Core.
The host injects `PrincipalResolver`, request metadata, the authoritative Run
loader, and an optional object-level `RunAuthorizer` before registering routes.
Passing a nil authorizer selects the fail-closed owner-only policy:

```go
shared := runtimehttp.NewShared(principalResolver, metadataResolver, runtime, runAuthorizer)
handler := runtimehttp.NewHandler(runtimehttp.Dependencies{
    Runtime: runtime,
    Workbench: workbenchQuery,
    Feed: runFeed,
    Shared: shared,
})
runtimehttp.NewModule(handler).RegisterRoutes(apiV1)
```

Object authorization stays at the Host/HTTP edge. Explicit policy denials are
rendered as not-found so a caller cannot probe another tenant's Run IDs.

`GET /runs/:run_id` returns the current aggregate plus `eventHead`, not the full
Kernel Event journal. Use `GET /runs/:run_id/events?afterSeq=...&limit=...` for
bounded durable audit/event history. `/runs/:run_id/feed` remains the separate
semantic Run Feed and is not used as a compatibility substitute for the journal.

The unique wire contract is `sdk/contracts/agent-runtime/v1/openapi.yaml`,
including the `harness` capability fragment and explicit cursor-expired
recovery headers for both Run Feed and Harness Turn Feed.
