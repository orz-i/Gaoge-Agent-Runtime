# Provider Transport — independent protocol module

Module: `github.com/orz-i/Gaoge-Agent-Runtime/go/provider-transport`.
Package name: `transport`. Initial version: **v0.1.0-beta.1 (unreleased)**.

This module owns wire encoders, decoders, streaming parsers and media operations
from Gaoge's 38 protocol adapters. It is physically moved from the former
`go/agent-runtime-provider/transport` package, not copied or re-exported. The
original source and fixture tests are preserved. It does not select user routes,
decrypt credentials, authorize media, bill users, persist requests or grant
permission to retry unknown external effects.

## Independence and versioning

Production code uses only the Go standard library. Tests use Testify. There are
no dependencies on Kernel, Agent, Harness, the Runtime provider module, Gaoge or
Agent Server. A repository location is not a dependency on its other modules.

This module has its own `VERSION` and future `go/provider-transport/v<version>`
tag namespace. Root Runtime releases do not tag or publish it. No module tag or
release is created by this extraction. Source consumers may pin a reviewed
remote commit containing this module; do not label a commit pin as a released
beta.1. After a separately authorized immutable module tag exists, normal Go
version selection applies. See [ADR 0019](../../docs/adr/0019-independent-provider-transport.md).

Change imports to the new module path and remove the old transport dependency.
There is no compatibility package, type alias, forwarding wrapper or duplicated
implementation at the old location. The optional Runtime-specific
`agent-runtime-provider` OpenAI/Anthropic `model.Client` adapters remain separate.

## Host responsibilities

The Host supplies an explicit `HTTPClientFactory`. Preserve its SSRF controls,
network policy and observability wrapper; a missing factory fails closed.
`RouteConfig` carries credentials only in memory. `GenerateInput` is transient
wire input, not a second durable Runtime request. A Host using a Runtime projects
its sealed canonical request and resolves immutable authorized artifact
references immediately before dispatch. Other Hosts need not import Runtime.

Text, image, video, audio, world/3D, embeddings and rerank operations retain their
distinct contracts. Media bytes belong only in the transient transport boundary.
Native token counting uses the same protocol encoding and never falls back to
generation. Original request/replay identity, safe single-attempt dispatch and
uncertain external effects remain the Host's responsibility; extracting this
module does not establish network exactly-once behavior.

## Verification

From this directory:

```sh
GOWORK=off go mod verify
GOWORK=off go test -race ./... -count=1
GOWORK=off go vet ./...
```

From the repository root:

```sh
make provider-transport-check
node --test scripts/provider-transport-policy.test.mjs
```

The checker copies only this module into an isolated directory, checks tidy,
runs the original suite and an explicit external consumer, then inspects both
the package and module dependency graphs. Its sole local replacement points to
the isolated standalone module, never SDK source. This is source validation, not
published-consumer validation. Once a reviewed candidate has been pushed, pass
`--revision=<full SHA>` to the checker to resolve the remote module without
replacements. `--published` separately verifies the actual independent version.

All deterministic original fixtures are retained. Gateway conversion assertions
remain the product integration's responsibility. Passing these tests does not
certify live provider compatibility, billing integration or paid effects.
