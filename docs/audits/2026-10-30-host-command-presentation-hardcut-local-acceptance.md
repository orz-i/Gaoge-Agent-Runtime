# 2026-10-30 — Host command presentation hard cut: local acceptance

- **Branch:** `governance/host-command-boundary-20261030`
- **Base:** `main@38b7106801e88779d4e9a41695d847c4ea5dcd5c`
- **Scope:** Harness executable child capability contracts, HTTP/OpenAPI and TypeScript client, local migration governance
- **Decision:** **GO for local atomic commits and independent PR admission review**; not authorization to merge or publish beta.24
- **Design authority:** [ADR 0014](../adr/0014-host-command-presentation-boundary.md), amending ADR 0012

## Code audit and boundary decision

The generic Runtime `IntentRouter` and implicit `Runner.Start` were previously removed. `SessionClient.Prompt` delegates to the explicit `StartAgentTurn`, and `ExecutionClass` records the selected execution owner for recovery; neither constitutes a text-to-Feature intent router. Provider Router selects an explicit frozen Model binding, not product task mode.

One remaining Host concern was demonstrably persisted in `CommandDescriptor`: `Trigger`, `Title`, `Description`, plus built-in `/team` and `/workflow` display and workflow-reference grammar. These UI choices changed immutable Config hashes without changing actual invocation authority. The HTTP `/harness/commands` and TypeScript discovery client exported the same Host-owned policy. This change removes those surfaces outright, with no rename or compatibility adapter.

Harness continues to freeze **only** explicit child capability identity, version, class, source, optional application kind and argument schema as `ConfigSnapshot.Commands`. `harness.invoke_capability` still validates the exact frozen command and original Tool Call ID before durable child execution. First-party tool-materialized children are limited to matching Team and Workflow classes; product Application children must identify their application kind. Existing Kernel, ModelInvocation, Tool receipt, approval/cancellation and Continuation authorities are unchanged.

## Verified local gates

| Gate | Result | Evidence |
| --- | --- | --- |
| `make check` on final source | **PASS, exit 0** | `/tmp/gaoge-host-command-boundary-final-check-20261030.log` (Mac workspace, temporary) |
| `make integration` with real Docker PostgreSQL and Redis | **PASS, exit 0** | `/tmp/gaoge-host-command-boundary-integration-20261030.log` (Mac workspace, temporary) |
| Harness full module `go test ./... -count=1` | **PASS** | local targeted rerun |
| HTTP full module `go test ./... -count=1` | **PASS** | local targeted rerun |
| Harness PostgreSQL adapter full module tests | **PASS** | local targeted rerun |
| TypeScript `typecheck`, tests and build | **PASS; 46/46 tests** | local targeted rerun |
| Strict legacy JSON descriptor rejection | **PASS** | `TestCommandDescriptorJSONRejectsHostPresentationAndUnknownFields` |
| GORM persisted legacy command rejection | **PASS** | `TestStoredLegacyCommandPresentationFailsClosedOnRead` |
| Removal from HTTP route, OpenAPI, TS client | **PASS** | route regression, OpenAPI contract, TypeScript compile/tests |
| `git diff --check` | **PASS** | local diff inspection |

The first `make check` invocation overlapped changes and is not relied on for final acceptance. The listed final check was started after the final code and contract edits and completed successfully. All tests are local, not an independent GitHub CI or product Host E2E claim. The real Docker integration suite started PostgreSQL/Redis, exercised restart/cross-connection/crash recovery, and cleaned up its containers and network.

## Upgrade risk and remaining boundaries

**Breaking Go, HTTP, TypeScript and persisted Config contract:** Hosts must replace built-in command defaults and command discovery with their own UI/capability composition. Old CommandDescriptors containing display fields are rejected on persisted config reads, rather than silently dropping those fields under unchanged hashes. Before activating beta.24, drain/reconcile/quarantine affected nonterminal Runs/Turns and stop incompatible workers; never forge a new Config hash or re-dispatch unknown external effects. Published beta.23 tags remain immutable. No Kernel status, SQL DDL, schema-version increment, new dynamic Feature registry, Host intent router or compatibility bridge.

Independent protected-PR merge admission, actual product command-menu/intent routing tests, live Model provider gateway verification and release gating remain separate. The two pre-existing untracked OpenAI Responses probe scripts in the working tree are unrelated and must not be staged or altered.
