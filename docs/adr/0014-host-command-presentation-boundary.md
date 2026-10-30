# ADR 0014 — Host owns command presentation; Harness freezes only execution authority

- **Status:** Accepted (pre-stable hard cut)
- **Date:** 2026-10-30
- **Target:** Unreleased beta.24 source cohort
- **Amends:** [ADR 0012](0012-host-selected-capabilities-and-external-model-providers.md)

## Problem

ADR 0012 already removed implicit user-intent routing and the generic
`Runner.Start` entry. Yet the Harness `CommandDescriptor` still persisted
Host presentation fields (`Trigger`, `Title`, `Description`) in an immutable
`ConfigSnapshot`, and the first-party SDK embedded `/team`, `/workflow`
trigger strings and a product-specific workflow definition-reference schema.
The HTTP `/harness/commands` and TypeScript DTO repeated these UI fields.

Thus a Host UI rename or slash-command change rotated the frozen configuration
hash, even though it did not change execution permission, Feature ownership,
Tool input validity or replay semantics. This is an unnecessary Host/Runtime
boundary leak, not a second execution mode.

## Decision

1. **Host selects and presents commands.** The Host owns the UI trigger,
   label, description, localized text, application menu, user-intent
   classifier and any per-product workflow definition-reference grammar.
   The Runtime no longer provides `FirstPartyCommandDescriptors`.
   Callers construct explicit `CommandDescriptor` values with their own
   stable command IDs and input JSON Schema. No implicit SDK commands exist.
2. **Harness retains only frozen execution authority.** The descriptor
   contains `ID`, `CapabilityKey`, `DefinitionVersion`,
   `ExecutionClass`, `Source`, optional `ApplicationKind` and
   `InputSchema`. A `CommandCatalog` is a validated, sorted, deep-copied
   set frozen into each Turn's `ConfigSnapshot`. It is not a generic
   registry or automatic user-text router. Remove the Host-facing
   `CommandCatalog.Resolve` convenience API; Hosts resolve UI commands
   using their own data.
3. **Closed typed executable child classes.** A first-party child command
   can describe only `ExecutionTeam` with `CapabilityTeam` or
   `ExecutionWorkflow` with `CapabilityWorkflow`. An application
   command must explicitly bind `ExecutionApplication` and its nonempty
   `ApplicationKind`. Agent and GroupChat cannot be selected through
   this child Tool union; they retain explicit Host entry points.
4. **No legacy snapshot reinterpretation.** JSON decoding of
   `CommandDescriptor` rejects unknown fields, including previous
   `trigger`, `title`, and `description`, rather than discarding
   them while trusting their prior `ContentHash`. Old nonterminal
   admission snapshots require deliberate drain or quarantine. No dual
   shape, renamed alias, compatibility converter, SQL migration or
   Kernel schema version increment is introduced.
5. **HTTP / TypeScript expose the same execution-only facts.** The
   Remove `GET /harness/commands` entirely rather than offering an
   execution-only replacement for a Host-owned discovery concern. Delete
   its HTTP Handler dependency, OpenAPI operation, TypeScript
   `harness.commands.list()` API and `HarnessCommandDTO`. Nothing is
   exposed under a compatibility route or alternate name.

## Ownership and exclusions

- Host: command discovery, UI/localization, slash dispatch, dynamic user-intent
  classification, configuration and product-specific definition lookup.
- Harness: immutable admission, Capability execution authorization and frozen
  schemas, Invocation IDs, Turn/interaction/approval/cancellation lifecycle.
- Feature: model/Tool/Workflow/Team semantics, original-Call-ID receipts,
  bounded retries and durable side-effect state.
- Kernel: revision CAS, epochs, journals and deterministic recovery.

Do **not** delete `ExecutionClass`, `Invocation`, `Provider Router`,
`ModelInvocation`, Tool effect intent or `SessionClient.Prompt` merely
because a prior audit called the surrounding Agent path `direct`.
`SessionClient.Prompt` delegates to `StartAgentTurn`; it is not a parallel
Agent engine. These objects have non-Host execution or persistence duties.

## Upgrade and tests

Existing Host code using `FirstPartyCommandDescriptors`,
`CommandCatalog.Resolve`, presentation fields on `CommandDescriptor`,
the HTTP `harness/commands` endpoint or the TypeScript command client must
change at compile time or update its HTTP calls. Host display metadata
and command discovery must live entirely outside frozen Runtime configs
and the SDK HTTP surface. For
old persisted Turn configs containing legacy fields, stop old workers,
inventory nonterminal Runs, and drain/reconcile/quarantine before deploying
this cohort; never alter a frozen Config hash to force a match or replay an
uncertain Model/Tool effect.

Regression admission requires: canonical descriptor JSON without Host metadata;
legacy JSON rejection in persisted `ConfigSnapshot` decoding; mismatch rejection
for first-party child execution classes; deterministic frozen/deep-cloned
command catalogs; Harness Capability Tool execution regression; GORM and Memory
config read/write; removal of the HTTP route, OpenAPI and Client API;
TypeScript compile/tests; relevant recovery and clean-client gates.
Live Host product command menus require a separate Host E2E check. Published beta.23 tags remain unchanged.
