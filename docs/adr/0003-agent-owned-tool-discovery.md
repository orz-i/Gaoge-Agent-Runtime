# ADR 0003: Agent-owned Tool Discovery for prelaunch evaluation

- Status: Accepted for isolated development only; not a release or host SDK-pin approval
- Date: 2026-10-08
- Baseline: `main@7c6804ff`
- Host decision: Gaoge ADR-0103

## Context

The Agent currently resolves all authorized local tools on every model invocation and eagerly provides every schema. The SDK has durable Kernel CAS and model invocation receipts but no candidate-set snapshot or loaded-set state. Gaoge is prelaunch, so no production usage is available; deterministic evaluation may establish correctness without pretending to prove provider token savings. Hosted provider tool activation is a separate, explicit grant and must remain so.

## Decision

- The owning boundary is `go/agent-runtime/agent`. Provide an optional deployment-composed local `ToolDiscovery` port and a read-only first-party `agent.search_tools` Tool. Hosts decide whether to compose it; when composed, SDK uses it automatically only above a fixed meaningful count of local candidate tools, rather than exposing new user settings.
- At Agent Run creation, freeze candidate local keys and the SHA-256 fingerprint of their canonical SDK `tools.Definition` (schema included in the hash, never stored in the snapshot). An immutable sorted candidate-set digest binds the discovery receipts to a Run. `ToolKeys` remains the authorization upper bound, including any explicitly activated hosted keys; search **never** adds new authorizations.
- Initially expose the control search tool, any `InitialToolKeys` (initially visible, **not required to invoke**), existing `RequiredToolKeys` (must eventually invoke) and explicitly activated Hosted Tools. Both local lists must be subsets of authorized `ToolKeys` and are frozen with the Run; Harness persists `InitialToolKeys` in direct Agent Invocation input for retries. A Search call receives an ephemeral `query`, frozen candidate identities/fingerprints and read-only names/descriptions freshly verified from the canonical SDK Catalog. The port returns key identities only. SDK validates results against the frozen set, re-resolves all loaded definitions to verify hashes/availability, and applies loaded keys, the receipt and result using one Kernel revision CAS. It must not execute a search as a business tool. The stateless, deterministic `LexicalToolDiscovery` implementation is suitable for prelaunch validation; its search correctness is not a guarantee of provider-side token savings.
- A business Tool is callable only if included in the prior Model Request's loaded definitions, and still passes regular policy, current catalog resolution, schema validation, approval and idempotent executor boundaries. Search Tool calls consume normal model/tool safety limits. Cancellation before CAS does not advance discovery state; CAS conflicts do not add tools or replay stale receipts.
- Search never indexes/discloses tools outside the frozen list. Duplicate tool labels or MCP server names cannot merge canonical keys. Provider-native Anthropic/OpenAI Tool Search remains out of scope.
- A frozen Role may separately declare `HostedToolGrants` with canonical Key and DefinitionVersion. These grants are added to **that child Agent only** after intersecting ordinary local ToolKeys with parent grants. They never become parent ToolKeys or a sibling grant. At child Start and each Model Request, Agent validates the hosted Tool's live catalog availability, pinned role model and exact DefinitionVersion. A disabled/incompatible/changed hosted Tool fails closed. This is a distinct child grant and does not bypass the administrator-owned Directory or approval/usage accounting.
- Existing Eager behavior remains the single default path when discovery is not composed. This is capability selection, **not** a compatibility fallback accepting old unauthorized payloads.

## Failure and validation

- Invalid/unknown/duplicate or oversized search results fail closed. Catalog disable/removed definition/schema changes fail closed before model exposure and before execution. The total frozen candidate ceiling is 256 so that 128 user-selected MCP tools plus authorized first-party controls remain within the SDK bound; 257 candidates fail closed.
- Search/query strings are model/tool transcript data, not part of the persisted **discovery receipt** or content-off telemetry. Search result only includes validated canonical keys and count. No credentials/host payload.
- Memory Run resume, pending model invocation replay, same Call ID idempotency, cancellation and child authorization tests are required; PostgreSQL conformance only if persisted schema contracts are touched. `InitialToolKeys` is persisted on the Harness Agent Invocation input and in the Agent's frozen discovery snapshot; unlike mandatory `RequiredToolKeys`, initial visibility must not force a call before completion.
- Deterministic MCP fixtures at 0/5/16/32/128 local tools; same-name collision, malicious port result, missing candidate and revocation; control model context and report local projected schema byte savings without claiming real provider tokens.

## Release conditions

No tag, no SDK pin, no automatic merging. Host integration needs its own contract review and explicit version adoption. Production optimization requires additional provider compatibility, task-accuracy and genuine usage evidence.
