# ADR 0019: Independently version Provider Transport

- Status: Accepted for implementation; no release implied
- Date: 2026-11-17
- Source baseline: `7e6703451d9c808d6820d7f224af808098091f78`
- Consumer decision: Gaoge ADR-0123 and Agent Server P1

## Context

The host-neutral provider wire adapters previously lived in
`go/agent-runtime-provider/transport`. Their production sources already use only
standard-library packages, but importing them requires the enclosing Runtime
provider module and therefore the synchronized execution SDK dependency graph.
Gaoge's Model Gateway cannot complete SDK extraction while that coupling remains.
The package has 38 adapter identifiers and existing deterministic text, stream,
replay, native-tool, usage and media fixtures. Replacing it with the narrower
Runtime `model.Client` OpenAI/Anthropic adapters would discard product capability.

## Decision

Physically move the complete transport package and its tests to
`go/provider-transport` in this repository, with module identity
`github.com/orz-i/Gaoge-Agent-Runtime/go/provider-transport`. Keep package name
`transport` and preserve the existing implementation and exported contracts.
Delete the old source location without aliases, forwarding packages or copies.
No production wire behavior, persistent state, database or network service is
added by this extraction.

The module is independently versioned by its own `VERSION` and future
`go/provider-transport/v<version>` tags. Its initial version is
`0.1.0-beta.1`, unreleased. Do not add it to `agent-runtime-boundary.json`'s
synchronized Go release cohort or the root Runtime tag generator. A shared
contributor workspace may test it, but independent builds use `GOWORK=off`.
A release tag remains a separately authorized operation.

The public module must not import or transitively require execution SDK,
Gaoge product or Agent Server modules. Its tests may depend on neutral testing
libraries. Enforce the actual package AND module graphs, rather than relying
only on import-name searches. Source consumer tests copy only this module out
of the repository. Remote-revision/published consumer tests contain no local
replacements. Keep the original protocol fixture suite in ordinary unit/race,
coverage and lint runs; an independent module must not silently lose CI coverage.

Model Gateway keeps route policy, provider credentials, entitlement and pricing.
Hosts retain outbound network policy, authorized immutable media resolution,
original request identity and ambiguous-effect reconciliation. No provider
microservice or Server-to-Gateway-to-Server loop is introduced. The Runtime's
existing `model.Client` adapters remain in `agent-runtime-provider` and continue
to follow that module's own synchronized release contract.

## Consequences

Transport consumers must update their Go imports and explicitly pin the new
module. Gaoge adoption is a separate reviewed change; this Runtime PR neither
updates Gaoge's installed SDK nor changes the new Agent Server's frozen Runtime
pin. Until consumer adoption is verified, do not claim full Gaoge SDK removal.
No historical execution migration, compatibility layer, release publication or
paid provider call is required or authorized by this source extraction.

Verification compares the moved Go files with the frozen baseline, runs the
original suite before and after extraction, exercises external public signatures
and deterministic transport behavior, checks independent module resolution and
negative dependency-policy fixtures. Live provider compatibility, all-product
integration and release qualification remain separate evidence boundaries.
