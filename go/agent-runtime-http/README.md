# Agent Runtime HTTP Adapter

`github.com/orz-i/Gaoge/sdk/go/agent-runtime-http` exposes HTTP v1 for Runs,
Events, Plan, Interactions, Checkpoints, Outputs, Evidence, Queue and Workbench.
The host injects `PrincipalResolver` and `RequestMetadataResolver` before
registering routes:

```go
handler := runtimehttp.NewHandler(engine, runtimehttp.Dependencies{
    PrincipalResolver: principalResolver,
    RequestMetadataResolver: metadataResolver,
})
runtimehttp.NewModule(handler).RegisterRoutes(apiV1)
```

The unique wire contract is `sdk/contracts/agent-runtime/v1/openapi.yaml`.
