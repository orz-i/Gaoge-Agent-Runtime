# Gaoge Agent Runtime

Gaoge Agent Runtime is a framework-neutral runtime SDK for durable, tool-using
agents. It separates the deterministic run state machine from optional agent,
harness, HTTP, protocol, and persistence capabilities.

This repository is the canonical public source. The current release line is
`v0.1.0-beta.8` and should be treated as a Beta API.

## Packages

| Package | Purpose | Beta support |
| --- | --- | --- |
| `go/agent-runtime` | Kernel, agent loop, tools, context, workflows, teams, queues, evaluation | Supported |
| `go/agent-runtime-harness` | Durable turn orchestration, interactions, timelines, delegation | Supported |
| `go/agent-runtime-http` | HTTP v1 handlers and SSE feeds | Supported |
| `go/agent-runtime-postgres` | Kernel and run-relation stores | Supported |
| `go/agent-runtime-harness-postgres` | Harness and Context V2 store | Supported |
| `go/agent-runtime-redis` | Durable continuation queue and run feed | Supported |
| `go/agent-runtime-mcp` | MCP client, registry, and transport adapters | Supported |
| `go/agent-runtime-a2a` | Explicit A2A v1 plugin, client/server edge, and durable shadow runs | Supported in Beta.2 |
| `ts/agent-runtime-client` | Dependency-free TypeScript HTTP v1 client | Supported |
| `contracts/agent-runtime/v1` | OpenAPI and capability contracts | Supported |

## Install

```bash
go get github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime@v0.1.0-beta.8
pnpm add https://github.com/orz-i/Gaoge-Agent-Runtime/releases/download/v0.1.0-beta.8/orz-i-agent-runtime-client-0.1.0-beta.8.tgz
```

Prereleases are distributed through Go module tags and GitHub Release archives.
The npm registry is reserved for stable releases; Beta publication does not
require npm credentials.

## Minimal Go runtime

```go
store := memory.NewStore()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
if err != nil {
    log.Fatal(err)
}

snapshot, err := runtime.Create(context.Background(), kernel.CreateRequest{
    Kind:   kernel.RunKind("agent"),
    Actor:  kernel.ActorRef{TenantID: "acme", ActorID: "user-1"},
    Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"},
    Goal:   "Prepare a release summary",
    State:  json.RawMessage(`{}`),
})
```

See [the Go quickstart](go/agent-runtime/examples/quickstart/main.go) and the
[TypeScript client guide](ts/agent-runtime-client/README.md) for runnable
examples.

## Compatibility

| Dependency | Tested baseline |
| --- | --- |
| Go | 1.26 |
| Node.js | 24 LTS or newer |
| pnpm | 11.22 |
| PostgreSQL | 16 |
| Redis | 7 and 8 |
| Agent Runtime HTTP | v1 |

Beta releases may make source-incompatible changes between prereleases. The
HTTP v1 contract and persisted record migrations will always receive an
explicit changelog entry and upgrade note.

## Verify from source

```bash
pnpm install --frozen-lockfile
make check
make integration
```

`make integration` starts isolated PostgreSQL and Redis containers, runs the
real-engine concurrency and recovery suites, and removes the containers.

## Project policy

- [Beta support policy](SUPPORT.md)
- [Security policy](SECURITY.md)
- [A2A product integration and support matrix](docs/a2a.md)
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)
