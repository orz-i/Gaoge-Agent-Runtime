# Agent Runtime P3.1 — Final Merge Audit (2026-10-08)

## Decision

**GO for a reviewed Fast-forward merge of SDK source into upstream `main`, subject to explicit user authorization.**

**NOT an authorization** to merge, push, tag, publish artifacts, or update the Gaoge SDK pin in this audit run.

- Audited upstream main: `c60c2a5b0e969172d71abc9a2c09932bf5e1ec13` (`fix(mcp): 恢复官方 SDK 协议协商 #33`), confirmed over GitHub HTTPS on 2026-10-08.
- Branch: `feat/tool-discovery-p31-runtime`, rebased cleanly on this upstream; 8 original P3.1 commits preserved.
- Canonical contract: [ADR 0003](adr/0003-agent-owned-tool-discovery.md); blocker and remediation record: [ADR 0004](adr/0004-tool-discovery-final-merge-audit-remediation.md).
- Host remains on its own `feat/tool-discovery-p31-host` branch and **still pins `7c6804ff` (beta.12)**. No automatic Host merge or SDK pin bump.

## Merge-blocker remediation

1. **Durable pending model requests bypassed grant revocation**: reproduced four Hosted grant/activation scenarios and one local Tool Search definition-drift scenario. Before the fix, the provider was invoked a second time after disabling or changing tools. SDK now re-resolves frozen grants and validates **each provider-visible local and Hosted declaration**, including provider payloads, both before processing a pending invocation and after concurrency admission, immediately before provider dispatch. Revoked requests fail closed as `agent.discovery_invalid`, with no second provider call. A valid unchanged Hosted request still preserves the exact logical Invocation ID over retry.
2. **Hosted Role grants without fixed Role model**: reproduced a successful invalid Harness seal. SDK now requires a pinned Role model when its snapshot declares Hosted grants. Parent, sibling and ordinary local Tool scopes remain separate.
3. **SDK search control could be shadowed as a Hosted Tool**: reproduced in 0-local-tool Eager Runs with and without a composed discovery port. SDK now reserves `agent.search_tools` at Agent Start for **all** local/Hosted combinations, independently of the 16-tool threshold.
4. **Delegated child context Tool was deferred when Tool Search activated**: Handoff now projects **only already-authorized Context-artifact read** to the child `InitialToolKeys`. The subset is checked and frozen in durable delegation; no forced `RequiredToolKeys`, arbitrary MCP preloading or new authorization. Pure Handoff forwarding/invalid-set tests, Harness context-scoping tests and existing initial-tool/resume tests pass.
5. **Main branch advanced during implementation**: branch was rebased to `c60c2a5`, with no file-level conflicts. The MCP official SDK protocol-negotiation patch is included in the audited ancestry.

## Tests and evidence

| Check | Result | Meaning |
| --- | --- | --- |
| `make check` on final P3.1 candidate | **PASS (exit 0)** | SDK metadata, gofmt/tidy/vet, all Go packages and race, Go lint, TS lint/typecheck/test/build, deterministic eval/benchmark smoke, OTel/A2A, independent release consumer |
| `go test ./... -count=1` in SDK MCP module | **PASS** | Official MCP protocol negotiation included from updated upstream |
| `go test ./agent` / `./handoff` / SDK Harness | **PASS** | 0/5/16/32/128-scale search, 128+first-party case, negative authorization and recovery, new child preloading |
| `go test -race` focused and full SDK `make check` races | **PASS** | Pending invocation, search and Handoff concurrency |
| `golangci-lint` affected packages and whole SDK | **PASS, 0 issues** | Static/runtime contract constraints |
| Temporary `GOWORK` with Gaoge Host branch, Go tests `./internal/bootstrap/mainapi/... ./internal/conversation/... ./internal/assistant/...` | **PASS** | Dev integration, **not** production SDK pin adoption |
| Git ancestor/diff/clean worktree | **PASS** | No branch drift or uncommitted changes |

The synthetic 128-tool projection benchmark compared **26,297 bytes** for Eager vs **371 bytes** for the initial Search Tool declaration. This is serialized SDK request data, **not** measured token billing, actual provider latency, task correctness or realized end-user benefit.

## Explicit exclusions and release order

- No real MiniMax provider tool call or financial activation was attempted. Provider-native Anthropic/OpenAI Tool Search remains out of P3.1 scope, not implicitly enabled by local lexical search.
- Historical Gaoge `harness entity not found` was not reproduced from screenshots or linked to a real persisted Run/Invocation. The separate Host error is **not** claimed fixed by these SDK tests.
- The change adds a source-level SDK contract; version/tag/release and Host submodule adoption must be authorized and audited separately.
- Recommended order: approve upstream source merge → recheck live `main`, Fast-forward merge/push by **separate user authorization** → select/pin the published SDK commit in the Host worktree → rerun Host without temporary `GOWORK` → independent Host merge audit.
- The GitHub remote `main` is the merge target; the local SDK main worktree is intentionally left unchanged. No unrelated worktrees or temporary environment are deleted as part of this audit.
