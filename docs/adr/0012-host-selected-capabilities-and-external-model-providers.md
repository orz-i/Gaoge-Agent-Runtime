# ADR 0012 — Host-selected capabilities and external Model Providers

- **Status:** Accepted (pre-stable hard cut)
- **Date:** 2026-10-30
- **Target:** Unreleased coordinated beta.24 cohort
- **Prior context:** ADR 0008/0009 (Kernel and recovery), ADR 0010/0011
  (frozen ModelInvocation and claim/retry governance), PR #72 recovery fences

## Problem

Gaoge is a durable, embedded-first Runtime. Product-level user-intent
classification, feature selection, Provider credentials and model endpoint
configuration are Host concerns; Kernel must not become an application router.
However, a persisted *execution intent* is not a user-intent classifier.
Removing a Model/Tool/Feature/cancellation intent or moving its authority to
an arbitrary Host plugin would destroy receipt, retry and crash-safety evidence.

The previous `harness.Runner.Start` entry selected the default direct Agent
without a feature-named entry point. The Provider Router, OpenAI and Anthropic
adapters shared the Core Go module with Kernel and Agent.

## Accepted decision

1. **Host selects the capability before admission.** The Harness public
   entry points are `StartAgentTurn`, `StartTeamTurn`,
   `StartGroupChatTurn`, `StartWorkflowTurn` and
   `StartApplicationTurn`. No catch-all `Runner.Start` or implicit
   `IntentRouter` is retained. A host may implement intent recognition,
   a router or an explicit UI menu *outside* the Runtime and invoke one
   selected entry. `SessionClient.Prompt` is an explicitly Agent-oriented
   convenience facade, not a generic dispatcher.

2. **Feature-owned execution intents stay durable.** Agent owns
   `ModelInvocation` (frozen request, Provider identity, Tool catalog
   fingerprints, lease, retry window and receipt). Harness owns the top-level
   Invocation/Turn admission, control arbitration and cancellation/dispatch
   evidence. Kernel owns revision CAS, Store Epoch and atomic transitions.
   A direct call, queue trigger or Host callback may request a resume but
   cannot bypass these owning facts. No generic intent store or second
   state machine is introduced.

3. **Concrete Providers are standalone Modules.** Core `model.Client`
   remains provider-neutral; `go/agent-runtime-provider` owns Router,
   OpenAI Responses and Anthropic Messages adapters. Hosts explicitly
   compose an adapter/router, select credentials/endpoints and register
   bindings. There are no Core forwarding aliases, dynamic Go plugins or
   Provider-as-MCP-Tool transport. A Provider is not authorized to rewrite
   frozen messages, Tool schemas or previously committed invocation state.

4. **Built-in Runtime Feature classes remain a closed safety contract.**
   Agent, Team, GroupChat, Workflow and Application have different
   construction, cancellation, approval, retry and recovery rules. The
   `ExecutionClass` discriminator is persisted proof of the selected
   execution owner, **not** a policy for choosing a Feature from user text.
   It remains exhaustive. Product-specific capabilities use the already
   statically composed `ApplicationCapabilityExecutor` and its optional
   read-only Reconciler. Adding a public generic FeatureDriver registry now
   would create an untested, unstable recovery protocol, not a safe
   extensibility gain.

5. **Provider binding identity is Host-owned and now frozen.** The Router
   preserves the public Provider name but additionally requires an explicit,
   nonsecret Host `Binding.Identity`. It combines this with the concrete
   Client's fingerprint when provided. Native OpenAI/Anthropic adapters HMAC
   the actual Endpoint, API credential, wire protocol and selected
   configuration. Custom transports require Host-controlled binding scopes.
   Agent freezes the SHA-256 fingerprint into its ModelInvocation and hash.
   Named Providers without a valid proof are rejected before a new intent;
   changed/missing proof on resume is rejected both before claim and before
   physical dispatch. Hosts must still rotate their explicit identity if a
   custom Client silently changes implementation/proxy semantics. Unknown
   outcomes require reconciliation or quarantine, never blind retry.

   **2026-10-30 implementation amendment:** the previously proposed
   fingerprint is now supported by same-name Endpoint/credential/policy drift
   tests, missing-proof tests and a committed-pending-intent crash scenario.
   No dynamic registry, alternate provider state machine or SQL schema version
   is introduced.

## Explicit exclusions and revision criteria

- No automatic classification, general-purpose `intent` mode, runtime
  service locator, dynamic plugin loader or compatibility `Start` shim.
- No generic FeatureDriver registration, open-ended persisted
  `ExecutionClass`, alternate grant ledger, duplicate recovery state or
  reclassification of existing Runs.
- No deletion, reinterpretation or automatic migration of frozen
  ModelInvocation requests, ProviderReplay, Tool receipts, Session Context
  or cancellation/dispatch claims.
- Reconsider a public Feature Driver only with an actual third-party
  Feature implementation that proves stable Run identity, crash-window
  no-effect/unknown-effect handling, cancellation, approval, leased
  replay and end-to-end Store conformance without private Harness bypass.

## Upgrade and evidence

The beta.24 API break removes `Runner.Start` and old Core Provider imports.
Update Host callsites explicitly. Stop and inventory in-flight Runs before
upgrading; drain or quarantine unrecognized Provider bindings rather than
rewriting or replaying committed requests. No SQL/schema upgrade is added by
this decision. Source-cohort module tests, external Go consumers, explicit
entry reflection regression and Kernel/Harness recovery regression suites
are the minimum local admission evidence. Published release verification
remains separately gated and beta.23 tags are immutable.
