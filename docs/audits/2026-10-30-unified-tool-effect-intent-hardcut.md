# Unified Tool effect intent and receipts — independent local acceptance

**Date:** 2026-10-30
**Base:** `main@1cd08e21ce225e637a806da807cea50d5ec02f6f` (PR #76)
**Branch:** `governance/agent-tool-effect-intent-hardcut-20261030`
**Reviewed code Head:** `9a9a57f8d50e7f8f603676153f95102e1ddf0237`
**Scope:** local implementation, negative recovery and Store boundary tests;
no push, PR, tag or release authority.

## Code review and fault evidence

### P1 — unleased direct Tool effect lost its receipt (commit d6f6118)

The earlier direct Agent (`RunExecution == nil`) executed a Tool before
persisting `ToolDispatchID` and did not universally validate returned Tool
receipt IDs. A faulted Store suppressing the `tool.completed` commit
**after** a physical Handler side effect left no durable pre-execution
evidence. Reopening the same Run could execute the same effect twice.
The regression `TestUnleasedToolReceiptLossCannotDispatchExternalEffectAgain`
was written against the old behavior before remediation.

All Agent modes now write exact original `Call.ID` dispatch intent via
Kernel revision CAS **before** Tool/Middleware invocation; an unknown,
malformed, mismatched or pending receipt retains the marker. Direct and
lease-backed recovery obey the same no-auto-replay rule. A trusted read-only
`ToolOutcomeVerifier` supports both. Exact matching receipts are consumed
only by the owning Tool call, not by fresh Model output or a replacement ID.

### P1 — read-only built-in Tool recovery and budget (commit 4834ca7)

Harness Capability/Delegation child Tools now expose read-only exact-child
verification. No verifier can call `Execute`, `Delegate` or restart a child
as a side effect. The Capability Tool receipt now binds
`Receipt.ExecutionID` to the original parent Model Tool Call ID; child
Invocation ID remains in structured content. The shared budget verifier
settles the original dispatched Tool reservation exactly once using the
same immutable Call identity and request hash; replay after a lost Agent
receipt CAS neither executes nor charges twice.

### Independent P1 — misleading post-dispatch correctable error (commit 6e97547)

Independent re-audit proved a narrower regression in the initial change:
a Handler physically mutates a remote record, then returns
`tools.NewRecoverableCallError`. The initial implementation cleared
`ToolDispatchID` and sampled the model again even though there was no
authoritative **no-effect** proof. The deterministic test
`TestPostdispatchRecoverableToolErrorDoesNotCertifyNoEffect`
**failed before correction** (physical effects 1; Model requests 2; no
`ErrToolOutcomeUnknown`).

The hard cut now requires an original-ID terminal
`ReceiptDispositionRejected` **and** a typed correctable error before
the Agent may request a model correction after physical Tool/Middleware
entry. This trusted Host Handler attestation is generated explicitly by
`tools.RecoverableNoEffectResult` for proven pre-effect refusals.
Otherwise Agent retains the exact dispatch identity and returns
`ErrToolOutcomeUnknown`. Core Registry and execution policy preserve
the signed-off no-effect result across their boundaries. Built-in Harness
policy/materialization denial uses the same rule, rather than a new
fallback error convention. Negative tests cover missing proof, wrong
CallID, wrong disposition, pending receipt and ordinary errors.

### Independent P1 — Dispatched/Held shared budget reentered Tool (6e97547)

The shared BudgetMiddleware previously allowed a **previously Dispatched
+ Held** reservation to enter `next()` a second time when its receipt
was missing. A new independent test proved physical Tool calls increased
from 1 to 2 on the same reservation even though quota stayed held.

The middleware now rejects all re-entry to an already dispatched Held
Tool reservation and retains it for read-only reconciliation. It settles
a correctly identity-bound `rejected` no-effect receipt as one attempted
Tool call; any unsupported/error-only proof leaves quota reserved and
cannot reexecute physical work.

## Execution boundary and deliberate limits

The logical order is:

`Intent -> Kernel CAS -> physical Tool -> exact terminal receipt -> Kernel CAS`.

This unifies the **logical Tool effect intent** and original-ID receipt
rules across direct and Store-leased Agents; `RunExecution` still provides
optional execution Epoch/lease ownership for multiworker contexts.
Unleased direct Agents are **not** certified as multiworker-safe simply
because their Tool intent and receipts are now durable.

Neither a Tool Handler's `rejected` attestation nor a remote COMMIT receipt
can be cryptographically proven by SDK code alone. Hosts must provide a
trusted provider-side lookup and stable physical effect identity. Timeouts,
lost network responses and cancellation after an external operation remain
outcome-unknown. No guarantee of physical exactly-once delivery is made.

This is a pre-stable, deliberately breaking Tool contract. Pre-hard-cut
in-flight Runs whose earlier physical side effect lacks a durable intent
or trusted receipt must be inventoried, drained/reconciled or quarantined
before activating the new code. No old retry bridge, synthetic receipt,
alternate effect ledger, generic FeatureDriver, SQL DDL, new Kernel status
or schema-version bump is added. See ADR 0013 and the beta.24 breaking
change ledger.

## Acceptance checklist

- Targeted original-effect crash and exact read-only receipt recovery:
  pass on implementation commit.
- Correctable post-dispatch false no-effect test: red before audit fix,
  green afterwards. Tool rejected/no-effect proof negative cases pass.
- Shared Tool Budget Held re-entry test: red before audit fix, green
  afterwards; exact rejected no-effect charge and idempotent verified
  receipts pass.
- 12 repeated race detector runs of Agent, Tool and Harness negative/positive
  cases pass.
- All 12 Go source module tests, format, Lint and metadata pass on code Head.
- Final exact-source `make check`, coverage, security, actual Docker
  PostgreSQL/Redis continuation and actual OTel Collector gates: recorded
  separately below when completed.

## Exact-Head full gates

All gates in this table were re-executed on exact **implementation Head**
`9a9a57f8d50e7f8f603676153f95102e1ddf0237`. The later audit-only
document commit does not change executable code.

| Gate | Result | Evidence |
| --- | --- | --- |
| `make check` | **PASS, exit 0** | All source Go module tests, race, fmt/tidy/vet, 12-module Lint (0 issues), TS test/lint/typecheck/build, evaluation and benchmark smoke, clean external Go + TS source consumers |
| `make coverage` | **PASS, exit 0** | Go statements **73.64%** (required >= 70%), TypeScript statements **88.08%**, TS branches **76.47%**, functions **92.85%**, lines **92.13%** |
| `make security` | **PASS, exit 0** | Zero detected reachable Go vulnerabilities; npm audit at high threshold clean. Scanner reports **three required dependency-module vulnerabilities without detected reachable call path**, not zero dependency vulnerabilities |
| `make integration` | **PASS, exit 0** | Actual PostgreSQL 16 and Redis 8 Docker services; cross-process Store recovery, cancellation, fencing, continuation, and real Process/Queue E2E; service/network cleaned |
| `make integration-otel` | **PASS, exit 0** | Actual pinned OpenTelemetry Collector contrib `0.149.0` emission and trace/metric E2E; service/network cleaned |
| Focused negative and positive retries | **PASS** | Twelve repeated `go test -race` iterations of Agent, Tool, Harness ambiguous-error, exact no-effect receipt and budget re-entry cases |

Operator logs (local non-published evidence):
- `/tmp/gaoge-tool-hardcut-final-gates-20261030.log`
- `/tmp/gaoge-tool-hardcut-final-check-20261030.log`
- `/tmp/gaoge-tool-hardcut-final-coverage-20261030.log`
- `/tmp/gaoge-tool-hardcut-final-security-20261030.log`
- `/tmp/gaoge-tool-hardcut-final-integration-20261030.log`
- `/tmp/gaoge-tool-hardcut-final-integration-otel-20261030.log`

**Local acceptance decision:** **GO for pushing the branch and creating a PR
with independent final-Head merge admission review**, upon explicit owner
instruction. **Not a merge authorization or release approval.** GitHub's
protected required checks must be verified on the actual final PR Head.
Do not create coordinated beta.24 release tags until separately authorized.
