# PR #87 — independent P1/P2 audit: Agent rejected-completion receipt proof and payload isolation

**Date:** 2026-11-03
**Immutable independently reviewed PR Head:** `3b17b683c5e13608dfb2f10961ae4c6dd81c1c7f`
**Scope:** Agent durable ModelInvocation under a typed `model.ResponseRejectedError`, with a named physical Provider binding. No PR merge or prerelease publication is authorized.

## Findings (confirmed RED on exact reviewed source)

### P1 — A known paid rejection can be settled without physical response identity

`go/agent-runtime/agent/model_invocation.go` accepted `model.IsResponseRejectedError(err)` plus nonnegative Usage as a completed billable ModelInvocation. It did not require a named physical Provider's `response.ResponseID` to exist or survive Agent's normalization. Thus an adapter that returned an **ID-less typed rejected completion** with Usage could cause the Agent to consume billed tokens despite no trustworthy physical completion identity. In the direct named synthetic Provider test, the original source continued terminal completion repair and counted the ID-less call as completed/billed.

**Permanent regression:** `TestNamedRejectedResponseWithoutPhysicalIDIsNotBillable` uses a named Provider with a frozen binding, returns a typed post-generation rejected first reply with a blank ResponseID and known Usage, and requires permanent failure, zero billed model invocations, no Tool execution and a still-pending unreceipted ModelInvocation. Only an explicitly named Provider with verified binding is subject to the new exact-ID requirement; unnamed local Models remain untouched.

### P2 — Rejected content and unauthorized ToolCalls persisted in durable receipts

The Agent-owned completed ModelInvocation path cloned the **entire original rejected Response** into `invocation.Response`. This bypassed the existing Provider helper's minimal-receipt semantics when a custom Provider or Model middleware returned a typed rejection with untrusted output. An existing synthetic fixture already returned `Content="rejected candidate"` and an unauthorized `ToolCall{ID:"forbidden", ToolKey:"not.authorized"}` together with a typed rejection and valid Usage. After Run termination or repair, both values were still visible in **Kernel-persisted ModelInvocation state**, even though they were never authorized for execution.

**Permanent regression:** `TestModelInvocationKnownRejectionHasOneToolFreeTerminalRepair` now asserts every durable rejected receipt excludes Content, local/hosted ToolCalls, artifacts, citations, ProviderReplay and refusal/continuation flags. The original source fails eight repair/rejection cases with persisted unauthorized Content/ToolCalls; normal repair/known-billing/restart behavior remains intact.

## Smallest fail-closed correction

At Agent's existing `executeModelInvocation` receipt boundary:

- Before accepting a **known typed rejection** from a named Provider with an immutable binding, require a nonempty `ResponseID` that remains unchanged under `strings.TrimSpace`. If it cannot prove physical completion identity, return `ErrInvalidModelResponse` without consuming Usage, creating completed receipt or dispatching ToolCalls.
- Before committing any typed known rejection, replace its entire `model.Response` with only the already supplied `ResponseID` and a cloned, valid `Usage`. Keep the existing typed error/classified error semantics for terminal correction and the prior same-Provider physical ID reuse check.
- Do not persist untrusted rejected content, ToolCalls, hosted Tools, signed/encrypted replay, citations or other response output. Do not introduce new execution opportunities or recover an ID-less response through retries.
- No changes to Kernel, Store/schema, Host, Provider wire protocols, replay version, recovery semantics or compatibility shims.

## Independent RED→GREEN verification

Read-only Go `-overlay -race` replaced **only** `go/agent-runtime/agent/model_invocation.go` with its exact immutable-Head version while retaining the new assertions: all rejected-payload isolation cases and the ID-less known rejection case failed. Corrected source passed the same focused unit suite plus existing terminal repair/restart cases under Race.

**Completed local verification — PASS:** full repository `make check` **exit 0** (Go Unit, Race, Vet, strict Lint **0 issues**, TypeScript lint/typecheck/**46 tests**/build, scenario/benchmarks, OTel/A2A and beta.24 clean-consumer cohort); real isolated PostgreSQL/Redis integration **exit 0** with temporary Docker resources removed; corrected focused Agent regression Race repeated **3 times**; gofmt and `git diff --check` PASS. Post-document `make metadata release-check` **exit 0**.

Fresh GitHub CI/security on the **new pushed PR Head** and a separate independent immutable-Head merge-admission review remain mandatory. The two preexisting untracked OpenAI probe scripts are excluded and unchanged; all model responses used for reproductions were synthetic, local, deterministic fixtures.
