# 2026-11-01 — Round 1: minimal single-Agent composition

- **Scope:** Core Agent / optional Harness integration only.
- **Base:** `main@0f8f9b7` (PR #83 already removed misleading Direct/Intent terminology).
- **Branch:** `governance/minimal-single-agent-20261101`.
- **Decision:** Keep the existing Agent Loop, Kernel and all durable execution
  proofs. Make the minimal composition understandable and executable without
  inventing a second facade, service locator, intent router or Session store.
- **Status:** Local implementation candidate; no remote PR, merge or release.

## Evidence before changing code

The repository already shipped a **Core-only tool-using Agent quickstart** at
`go/agent-runtime/examples/quickstart/main.go` and a full experimental
`harness/examples/task-agent` with advanced compaction, response replay and
recovery. Adding another Core quickstart or generic Runner factory would have
duplicated existing work.

`harness.NewRunner` required four non-nil ports even for an ordinary Agent:
`Runtime`, `Agent`, `Store` and `Clock`. Only the clock provides Harness
record timestamps; it does not authorize a Kernel execution claim. Host
sessions additionally require a Context Manager, but their transcript and
Model request projection must remain explicitly host-selected.

## Minimal composition contract

| User need | What runs | What stays absent |
| --- | --- | --- |
| One model/Tool loop | `kernel.Runtime` + `agent.Runner`, with explicit `model.Client` | Harness, Context, Workflow, Team, continuation workers |
| Host Turn / multi-turn text Session | Above + Harness Store, `harness.Runner`, Context Manager, `MaterializeContextWindowRequest`, Host `TranscriptSource` | Secondary model loop, implicit intent router, dynamic Feature registry |
| Process restart / concurrent workers | Same components plus appropriate persistent Stores, rehydration, frozen binding, execution leases and reconciliation | Blind retry of unknown external effects or hidden startup services |

### Implemented deltas

1. `harness.Dependencies.Clock` is optional. Nil uses a private system-clock
   implementation for Harness record timestamps; explicitly supplied test or
   production clocks retain their existing behavior. No new exported public
   symbol is needed. Kernel Store time and Epoch/Lease/Receipt authority remain
   unchanged.
2. Add `harness/examples/session`: runnable Host-owned two-turn transcript
   with **one** Agent model loop and frozen pre-commit Context projection. It
   verifies identical Turn admission replay and terminal Resume cannot
   dispatch more model requests.
3. Improve the existing Core quickstart to print its textual final answer
   rather than Go's `ContentPart` slice representation. Keep the existing
   real Tool invocation and original-ID receipt intact.
4. Add test coverage for default-clock single-Agent Harness composition,
   stable Turn idempotency, changed-admission rejection, opt-in Context, both
   examples' output, and the absence of duplicate model calls.
5. Update the root README, Agent task guide, architecture map, changelog and
   unreleased beta.24 notes. **No schema migration**, wire contract, runtime
   factory, legacy alias or compatibility bridge is introduced.

## Safety boundaries explicitly not relaxed

- Context is still opt-in. A nil Context Manager cannot open a Session,
  and model-visible Context injection must be composed as a trusted
  `PrepareModelRequest` stage before committing the immutable request.
- Same Turn ID with changed input still fails closed; `Prompt` is not an
  implicit retry or a Host transcript update operation.
- Concurrent multiworker execution still requires a supported
  `agent.RunExecution` / Kernel Store Epoch. Raw Tool effects continue to
  require durable original-Call intent and verified receipts even without it.
- Standalone Memory examples are in-process only. No external LLM, gateway,
  PostgreSQL/Redis, cross-process crash or mobile production readiness is
  claimed by these samples.
- Authorization and tenant-scoped transcript access remain Host obligations;
  this sample does not create an authorization policy.

## Validation

- `go test -count=1 ./...` in both Core and Harness modules: **PASS**.
- `go test -race -count=1 ./...` in Harness: **PASS**.
- `go test -race -count=1 ./agent ./kernel ./context ./examples/quickstart`
  in Core: **PASS**.
- `go run ./go/agent-runtime/examples/quickstart`: expected Tool-derived
  answer and terminal Run, **PASS**.
- `go run ./go/agent-runtime-harness/examples/session`: 1 then 2
  model-visible User turns, replay without duplicate delivery, **PASS**.
- `git diff --check`: **PASS**.
- Repository-wide `make check`: **PASS, exit 0** on the final source after
  fixing one new example-only `errorlint` complaint (`%v` was replaced by a
  separate `%w` error branch). Includes all Go modules' test/race/lint, Go
  workspace format/tidy/vet, TypeScript lint/typecheck/46 tests/build,
  evaluation smoke, benchmark smoke, OTel smoke, A2A product and local-cohort
  release consumer checks. The first full invocation stopped at that single
  lint issue; the complete second invocation passed.
- No external Provider call, real DB integration, `make beta`, release
  tag, remote push or merge was executed.

**Review criterion:** Future simplification must eliminate a demonstrated
second authority or Host dependency without removing original Model/Tool effect
intent, Store-fenced execution, cancellation grants, or exact replay identity.
The next round is lifecycle/State projection evidence, not permission to
eliminate the Kernel or merge Turn and Run statuses by intuition.

## Independent audit P2 — historical Turn replay remediation

The independent review found that the minimal demo's `TranscriptSource`
materializes the *current* Host history for each call. The original example
proved immediate `Prompt` replay before appending the current Assistant answer,
but did not demonstrate the correct read path once later Turns had advanced
history. A scratch replay of the first Turn after the second Turn completed
returned `harness.ErrConflict`; an explicit `Load` of that first Turn succeeded.
This is correct immutable `AdmissionHash` behavior, not a second Agent dispatch.

**Scoped correction:** The same demo now retains its first committed snapshot,
completes both Turns and appends both Assistant answers to Host history, then
uses `session.Load(ctx, oldTurnID)` to read the original output and checks that
no extra Model call occurred. An explicit negative regression check verifies
that calling `Prompt` for the old Turn with *changed* Host history still returns
`ErrConflict` without sending another Model request. The immediate identical
`Prompt` retry and terminal `Resume` checks are retained. The example README
and Agent task guide distinguish new submission, exact admission retry and
historical read. No API, Store, Kernel, Provider, Model request hash, execution
claim or compatibility behavior was changed; the example does not persist a
second transcript or a cache of old prompt requests.

**Verification:** the named delayed-replay scenario passed 10 repetitions;
the Harness module and Session sample passed `go test -race -count=1 ./...`;
Harness `golangci-lint run ./...` returned `0 issues`; `git diff --check`
passed. A fresh full repository `make check` completed with **PASS, exit 0**
on the corrected branch; this covers the entire Go module test/race/lint
matrix, metadata, formatting/tidy/vet, TypeScript lint/typecheck/test/build,
evaluation/benchmark smoke, OTel smoke, A2A checks and local-cohort clean
consumers. Independent Docker integration, live Providers and release
publication were not part of this scoped remediation.
