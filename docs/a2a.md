# A2A product integration

`go/agent-runtime-a2a` is the optional A2A v1 edge for Gaoge Agent Runtime. It
does not change the runtime architecture: the deterministic kernel remains
protocol-neutral, while a host explicitly composes the A2A plugin and owns all
product policy.

## Architecture boundary

The dependency direction is fixed:

```text
product host
  ├─ remote-agent directory, credentials, SSRF policy, auth and persistence
  ├─ finite child-route resolver
  │    ├─ local member -> local child runner
  │    └─ a2a:<public-id> -> A2A plugin -> durable shadow runner
  └─ optional A2A server handler mounted on a host-owned router

agent-runtime-a2a plugin
  ├─ official A2A wire protocol and Agent Card projection
  ├─ client/server adapters
  └─ shadow-run lifecycle mapping

agent-runtime kernel
  └─ protocol-neutral runs, checkpoints, events and narrow feature ports
```

The plugin is statically constructed with `NewPlugin`. It does not scan for
code, register globally, open a listener, or resolve arbitrary services.
`handoff.NewRouted` accepts a finite host-owned `ChildRunnerResolver`; only the
reserved `a2a:` member prefix may be delegated to this plugin. Unknown or
malformed reserved targets fail closed.

The host must implement `BindingResolver`. A binding contains an immutable,
non-secret discovery revision and a request-scoped client. The first remote
execution freezes the binding revision into the shadow Run. Recovery requests
that exact revision, so an Agent Card update cannot silently change an existing
durable run.

## Beta.2 support matrix

| Surface | Support | Notes |
| --- | --- | --- |
| A2A protocol | 1.0 | Exact version validation |
| HTTP+JSON | Supported | Client and server |
| JSON-RPC | Not in Beta.2 | Fails closed during interface selection |
| gRPC | Not in Beta.2 | Fails closed during interface selection |
| Agent Card discovery | Supported | Bounded parsing, cache validators and optional signature policy |
| Send Message | Supported | Task or direct Message response |
| Send Streaming Message | Supported | Ordered status and artifact events |
| Get, Cancel and List Tasks | Supported | Owner/tenant scoped when hosted |
| Subscribe to Task | Supported | Durable replay followed by bounded live SSE |
| Text, data, file bytes and file URL parts | Supported | Size and count limits apply |
| Input-required and auth-required | Supported | Persisted as resumable shadow waits |
| Push notifications | Not in Beta.2 | Capability is not advertised |
| Extended Agent Card | Not in Beta.2 | Capability is not advertised |

Skipped JSON-RPC, gRPC, push-notification, extended-card and unadvertised
extension cases in the official TCK are intentional scope exclusions, not
fallback paths.

## Product responsibilities

The SDK deliberately does not provide a global remote-agent registry. A product
host owns:

- stable public target IDs and the active/inactive lifecycle;
- encrypted credentials and request-scoped header injection;
- endpoint allowlists, DNS/IP validation, redirect policy and egress controls;
- immutable public Agent Card revisions and optimistic configuration updates;
- authenticated tenant/subject mapping and a durable `HostedTaskStore`;
- bounded descriptions exposed to models and the user-facing trust boundary;
- explicit mounting of `Host.Handler()` on an existing router.

Do not persist credentials in `Binding`, `Discovery`, shadow state, task JSON,
events, logs, model context or Agent Card revisions. Refresh credentials at
request time without changing a frozen public discovery revision.

## Security defaults

- Client endpoints are validated before discovery and every request. Redirects
  are host policy and should remain same-origin unless explicitly approved.
- Reserved protocol headers cannot be supplied as arbitrary credentials.
- Production hosting requires HTTPS, an authenticator, a durable task store and
  declared Agent Card security schemes/requirements.
- Hosted tasks are owner- and tenant-scoped. Persistence updates use optimistic
  concurrency and retain the official wire representation at the edge.
- Subscription authentication happens before task lookup. Missing, foreign and
  terminal tasks fail before an SSE success response is opened.
- Remote errors persisted by the shadow runner are bounded generic summaries;
  transport bodies and secrets are not durable state.

## Upgrade from Beta.1

Beta.2 turns the experimental adapter into an explicitly composed product
plugin. Existing direct client use remains available, but product integrations
should make these changes:

1. Reserve `a2a:<public-id>` for remote handoff routes and use
   `handoff.NewRouted` with a finite resolver.
2. Construct `Plugin` with a product-owned `BindingResolver`; persist and
   resolve immutable discovery revisions.
3. Move credentials, SSRF decisions and remote-agent availability out of
   runtime state and into the host control plane.
4. For server exposure, supply an authenticator and durable `HostedTaskStore`,
   then mount `Host.Handler()` yourself. Production mode rejects implicit
   in-memory or unauthenticated hosting.
5. Treat unknown remote states and changed/missing frozen bindings as failures;
   do not fall back to a local runner.

## Verification

The repository pins the official A2A TCK commit in
`scripts/run-a2a-tck.mjs`. The product boundary and focused regression suite run
with:

```bash
make a2a-product-check
```

For the networked official conformance run, install `git`, `go` and `uv`, then
run:

```bash
make a2a-tck
```

Set `A2A_TCK_DIR` to reuse a checkout already pinned to the required commit, and
set `A2A_TCK_REPORT_DIR` to copy the JSON, JUnit and HTML reports into an
external evidence directory.
