# 2026-11-07 — Provider/Context native input metering and ambiguous-evidence pilot

## Scope, baseline, and status

- Development worktree: `governance/provider-context-evidence-20261107`, stacked on PR #96 `e806db0` (Context Artifact candidate retrieval); original primary checkout `main@e933ac1` and its two user-owned untracked Provider probe scripts remain untouched.
- The first paid official OpenAI `evidence-40-v1` report (PR #95 evidence) was **37/40**, with `distractor-01/05/07` failures. This result is preserved; PR #96's candidate Tool and this branch's native accounting are **new measurement contracts**, not a retrospective score correction.
- Scope is restricted to optional OpenAI Responses input counting, Host-only Context wiring for the 40-case quality profile, a bounded `distractor-05` live-pilot entry point, explicit token-budget planner, negative oracles and documentation. No Kernel/Agent state changes, SQL DDL, additional router, persisted authority, dynamic Host intent routing, backwards compatibility bridge, or second Agent Loop.
- **No live paid Provider task or actual remote input-count request was made during this branch's implementation.** Only loopback native-wire fixtures and scripted Agent tests ran. The Mac Pi session has neither a trusted API key nor a Host model profile configured.

## Security invariants × boundary and failure path

| ID | Invariant | Path and adversarial cases |
| --- | --- | --- |
| C1 | Native count must describe the *same physical input* that the original Provider would generate with, including Tool declarations and opaque reasoning replay | OpenAI adapter reuses `prepare` for Model, input, Tools, Tool choice, strict output contract and reasoning. Encrypted reasoning and original local Tool call IDs preserve order; changing a replay CallID denies counting before any HTTP. No separate mutable count serializer or provider registry |
| C2 | No new credential forwarding or different Provider | Counting endpoint is the configured `/responses` URL's same-origin `/input_tokens` sibling. Arbitrary paths, redirects, wrong routed Provider/model, malformed JSON, duplicate keys, unexpected fields, aliases, or extra JSON reject/fallback. OpenAI protocol and exact Host-selected native model are checked before HTTP |
| C3 | Only an authenticated, positive and bounded native count is admissible as exact; absence is *not zero* | Reject missing, zero, negative, fractional, overlarge, duplicate, wrong-object, non-JSON, HTTP failure and unsupported endpoint. Context's original conservative hard-byte bound remains the fallback and preserves fail-closed admission |
| C4 | Token counting cannot authorize execution or conceal incomplete Usage | `model.InputTokenCounter` is independent of `model.Client.Generate`; a count is not a Model receipt, Tool effect, success or monetary charge. Optional `TokenAdmissionPlanner` additionally requires a positive enforceable output ceiling; Model BudgetMiddleware still suspends unknown Usage and honors original Run/Invocation CAS |
| Q1 | The second pilot must execute at most the selected *original* `distractor-05` case | Host-only `baseline-distractor-pilot-stdin` and Python `pilot-distractor` command. Untrusted stdin cannot choose case; the child sends `pilotCaseID`, parent checks its expected value; sample, requested count, repetition and summary are strictly bound |
| Q2 | A probe/pilot cannot be passed off as a full score or compared numerically | Pilot preflight is separate; `requested=40`, `attempted<=1`, `skipped>=39`, `complete=false`. The historical `code-01` pilot and two-case dataset remain unchanged. Quality OpenAI hash rotates from PR #96 `sha256:2ec73b1a92858853ba01a6f2999be07b2501b1c1a34c6c07ef93375df904a92e` to `sha256:d22c61a16559e72d0c75c8f61ba342ac16c9382d3a55afc6a5622d8d4f70624b` |

The OpenAI count endpoint is documented as exact for supported Responses inputs: https://developers.openai.com/api/docs/guides/token-counting and https://developers.openai.com/api/reference/resources/responses/subresources/input_tokens/methods/count. Anthropic's https://platform.claude.com/docs/en/build-with-claude/token-counting explicitly calls its count an **estimate**; it does not authorize removal of Context's hard upper bound.

## Implementation and verification

- Core: optional `model.InputTokenCounter` only.
- Provider: strictly routed `provider.ContextTokenCounter`, native OpenAI `CountInputTokens`, optional concrete/Router `PlanTokenAdmission`, same-origin count URL, fail-closed typed reply validation and no automatic retry-to-generation.
- Host example: opt-in Counter only for OpenAI `evidence-40-v1`; legacy baseline unaffected. Single-case ambiguous-evidence entry, report ID fencing and dataset hash change. No count calls are mixed into Model generation receipts.
- Loopback negative matrix: 9 malformed/unsupported count responses, redirects, rejected effort, encrypted Tool/reasoning replay, forged/foreign CallID, cross-model/protocol, unknown JSON fields, alias/trailing input, missing native token counter, positive-output TokenAdmission admission, native-count-fallback conservative Context and actual pilot Context wiring.
- Local passes: 12 Go workspace module unit suites plus `go vet`; Provider and Agent/Context/Model targeted `-race`, task-agent complete `-race`; 95 Python launcher/protocol tests; metadata/format/tidy; all-module lint 0 issues; eval smoke, portable benchmark smoke, release clean-consumer (46 TypeScript tests). `git diff --check` clean.
- Local deterministic 40/40 scripted calibration unchanged (`sha256:bf5d49af98d408c382aeb29c02514d29be370f636a136cae54c94b48f5364934`). This proves fixture/Oracle and recovery only, **not** real-model accuracy.

## Risk and next gate

1. **OPEN — physical compatibility:** the official Provider/native count endpoint has not been tested with the committed code or a supported third-party gateway. Count may be rejected on nonstandard proxy routes; fallback must remain safe.
2. **OPEN — measured outcome:** the original first real `37/40` is the only complete measured quality score. No evidence yet proves PR #96's candidate search + native count improves real `distractor-05`, repeated reads, token usage or end-to-end latency.
3. **OPEN — instrumentation:** the profile specifies an exact-count policy, but the current task report does not separately show per-request native count success, fallback count, latency or quota usage. A new real experiment must record those facts before quantitative performance claims, without storing raw prompts/keys/ciphertext or treating counting traffic as generation calls.
4. **OPEN — statistical power:** one case and one repetition are a bounded *diagnostic*, not an effect estimate. Any comparison after a changed Tool/accounting contract must disclose both dataset hashes and cannot be called a same-profile uplift.
5. **OPEN — release gates:** this is a stacked local candidate requiring locked final commit, independent review of that commit, GitHub PR-Head CI, and any required Docker/integration/security gates. This document is a local scope review, not independent merge approval.
6. **Requires opt-in:** with a trusted local `GAOGE_MODEL_PROFILES_FILE` and a separately entered hidden API key, run `make agent-quality-distractor-pilot`; it may incur model charges (preflight + at most 12 task Model calls). Do not auto-retry a failed pilot as 40 cases. No secret should be shared in chat or committed.

### Exit criteria

The Provider counting wire contract must pass real native endpoint preflight for an exact Host-certified model; a 40-case quality baseline is not approved until individual samples and independent cross-profile comparisons are preserved with their correct status. The security contract already has offline proof: a count failure never lowers the hard Context gate, a single-case pilot never starts unrequested tasks, and no unsupported Provider/count route can silently grant or dispatch a Tool.
