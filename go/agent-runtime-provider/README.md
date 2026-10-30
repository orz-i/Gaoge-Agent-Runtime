# Standalone model Provider module

This **optional** Go module implements the Core `model.Client` contract
without coupling Kernel, Agent, or Harness to vendor APIs:

- `provider.NewRouter`: immutable, explicitly bound Provider routing
- `provider/openai`: OpenAI Responses client
- `provider/anthropic`: Anthropic Messages client
- `provider/internal/providerhttp`: private bounded HTTP/SSE transport

Host code supplies credentials and authorized endpoints. There is no global
registry, dynamic loader, generic MCP transport, or old Core import alias.

From the repository root, run the model/Tool example with explicit credentials:

```bash
cd go/agent-runtime-provider
GAOGE_PROVIDER=openai GAOGE_MODEL=gpt-4.1 OPENAI_API_KEY="$OPENAI_API_KEY"   go run ./examples/providers "Echo hello and report the result"
```

Run `go test ./...` for vendor fixtures and cross-module Agent execution
tests. For signed/encrypted replay, HTTP errors and limitations, see
[Provider adapters](../../docs/provider-adapters.md). The first distributable
version of this module is part of the **unreleased beta.24 candidate**; never
attempt to substitute the already published beta.23 Core module or silently
switch Provider bindings for a pending ModelInvocation.
