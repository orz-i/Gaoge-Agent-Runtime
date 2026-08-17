# Agent Runtime HTTP Adapter

`github.com/orz-i/Gaoge/sdk/go/agent-runtime-http` exposes HTTP v1 for Runtime
Runs and Workbench resources. The first-party `harness` submodule exposes the
Harness Turn snapshot, semantic feed and approval continuation resources on
the same versioned wire surface without making Harness part of Runtime Core.
The host injects `PrincipalResolver` and `RequestMetadataResolver` before
registering routes:

```go
handler := runtimehttp.NewHandler(engine, runtimehttp.Dependencies{
    PrincipalResolver: principalResolver,
    RequestMetadataResolver: metadataResolver,
})
runtimehttp.NewModule(handler).RegisterRoutes(apiV1)
```

The unique wire contract is `sdk/contracts/agent-runtime/v1/openapi.yaml`,
including the `harness` capability fragment and explicit cursor-expired
recovery headers for both Run Feed and Harness Turn Feed.
