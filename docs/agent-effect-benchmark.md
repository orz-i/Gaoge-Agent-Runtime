# Agent effect benchmark — evidence-quality v1

**Status:** opt-in beta.24 development suite (2026-11-07). Not a general Agent benchmark, leaderboard, paid-provider validation, or release gate.

## Goals and scope

The existing two-case `release-recovery-v1` real-model baseline stays the default. The additional **40-case `evidence-40-v1` suite** samples the same real Agent + Harness + Kernel, Provider, Tools, archived Context and interrupted-execution recovery path, with an immutable expanded corpus. Every case requires the actual model to find an exact deep source fact, read its bounded Context Artifact, save exactly one draft and produce a schema-valid answer consistent with committed evidence.

The 40 cases are **eight examples in each of five variants**:

| Group | Variation exercised |
| --- | --- |
| `code-01..08` | Distinct deployment codes and numeric/alphanumeric facts |
| `window-01..08` | Date/time/timezone notation, including Unicode punctuation |
| `distractor-01..08` | Unrelated release records before and after the target |
| `unicode-01..08` | Non-Latin identifiers and multilingual surrounding text |
| `layout-01..08` | Irrelevant archive/index/trailer content around the exact record |

All variants remain within **one evidence-lookup-and-draft task family**. These are not 40 different end-user workflows and do not measure software engineering ability, browser navigation, open-ended research or agent autonomy. Expanding to such task families is separate governance, not grounds for inflating this score.

## Offline calibration — no credentials or network

Run:

```sh
make agent-quality-calibrate > /tmp/agent-quality-calibration.json
# Or verify deterministic oracle fixtures in tests:
go test ./go/agent-runtime-harness/examples/task-agent -run '^TestQuality' -count=1
```

The calibration uses the existing scripted model and the same real local Agent/Tool/Context/recovery machinery as the live version. It emits a JSON `scripted_oracle_calibration` report with 40 individual observations and a summary. This validates the task fixtures, Oracle, immutable IDs, Tool effects and interruption/recovery paths, **not the quality of any real LLM**. Scripted usage receipts are unknown where missing: never interpret a null subtotal as zero input tokens or cost.

## Safe, opt-in Provider-only preflight

To verify Schema and stateless Tool/reasoning replay without starting a single quality task, use:

```sh
make agent-quality-probe
# Or reuse an earlier failed live report's public endpoint/model/budget:
python3 scripts/run-agent-baseline.py probe artifacts/agent-baselines/<report>.json
```

Both commands still require an interactive terminal, a fresh hidden API key and Host-certified model capabilities. They may incur charges for **one unary strict-Schema request and up to two synthetic Tool/replay requests**; they categorically do not run the 40–120 evidence tasks. On success the report says `preflight_only_passed`, stores `preflightOnly=true`, records attempted=0/skipped=40 (or the configured sample count), and retains `successRate=null`. These reports are diagnostic evidence, **not** task-success observations; numerical comparison with a quality result is blocked. `retry` explicitly refuses a preflight-only report to prevent an accidental transition into all paid tasks. Reuse the report with `probe` to check again, or issue an explicit `quality` command when ready to budget the full suite.

This mode is enforced by a separate Go command path (`-mode=baseline-preflight-stdin`) which returns after the shared preflight and summary, before the first call to `runTask`. The ordinary `quality` path is unchanged. No API calls are launched automatically by CI, and an already-failed preflight cannot be resumed as if it produced committed task effects.

**First real-probe evidence (2026-11-07):** the official OpenAI Responses endpoint with `gpt-5.6-terra`, medium reasoning and output ceiling 4,096 successfully completed the strict-Schema preflight and both synthetic Tool/replay HTTP requests. The saved local report `20261107T045702Z-143d389d.json` was validated as `preflight_only_passed` with `reasoningSeen=true` and `encryptedReplaySeen=true`, complete Tool/replay Usage receipts, **0/40 task samples attempted**, and an explicitly unknown quality success rate. The local source was dirty; this is evidence of live compatibility for that checkout, not an immutable PR-Head/CI sign-off or a measured Agent task result. See [dated verification record](audits/2026-11-07-agent-effect-benchmark.md).

## Opt-in real-model measurement

```sh
make agent-quality
# Equivalent:
python3 scripts/run-agent-baseline.py quality
# Later, with the originally recorded configuration and a newly hidden key:
python3 scripts/run-agent-baseline.py retry artifacts/agent-baselines/<report>.json
# Compare compatible complete reports without any network requests:
python3 scripts/run-agent-baseline.py compare artifacts/agent-baselines/<old>.json artifacts/agent-baselines/<new>.json
```

The interactive CLI collects a verified Provider endpoint, exact model ID, documented Context capacity, output cap, 1–3 repetitions per case (40–120 paid task samples), optional reasoning effort, and finally a **hidden** API key after compiling the current checkout. The existing strict Schema and (when OpenAI) Tool/replay preflight must pass before any task starts. Preflight calls and their usage are separated from measured tasks. All real calls are opt-in and may incur significant external charges.

The Host remains responsible for correctly certifying model capabilities in `GAOGE_MODEL_PROFILES_FILE`; the SDK never guesses support from a model name or silently changes a failed request into a more permissive one. The Quality switch cannot be injected by a malformed/case-folded `suite` field. This suite never changes Kernel state machines or Agent execution policy.

If a new model exits preflight with `capability_unavailable` and zero observed HTTP responses, check the **Host-side capability catalog first**, rather than assuming the Provider rejected JSON Schema. Choosing `y` for an unknown model only permits a Schema probe. The benchmark also requires a positively certified `SupportsForcedToolChoice` and, when an effort is selected, that exact value in `EffortLevels`. Provision a trusted profile for the exact model ID, outside the repository, and export its path in the shell running `make agent-quality` (see [model profile setup](agent-model-baseline.md)). Do not bypass the capability gate or insert API credentials in the catalog. The model's **maximum supported** output size is not a sensible per-call task budget: start with roughly 1,024–4,096 tokens and increase only when actual failed samples justify it.

### Diagnosing OpenAI tool/replay preflight failures

If strict JSON Schema succeeds but `tool_probe_provider_failed` occurs at `tool_call`, a visible `response.output_item.done` and `response.completed` in a **redacted** SSE body do not establish that the original opaque `encrypted_content` values agreed. The report's `[REDACTED]` replacements intentionally discard those bytes, so replaying that sanitized body cannot certify the original physical stream.

For a new failed probe, the optional `toolProbe.wireEvidence` records only already-observed structural facts: `reasoningDone`, `encryptedDone`, `terminalSeen` and, **only when two same-item ciphertext values were present**, a boolean `doneFinalMatch`. Neither ciphertext nor reasoning item ID/hash is saved or displayed; partial or missing evidence is unknown rather than false authority. A false comparison proves that the two opaque ciphertext strings differed, **not** that the response was invalid: OpenAI documents the completed `response.output_item.done` reasoning item as the source for stateless continuation and does not require its ciphertext to equal the later `response.completed` copy. The Provider uses the completed done item's encryption after matching its output index/ID and every other field; explicit completed evidence from `response.output_item.added` must still agree, and Tool IDs/arguments/statuses, response IDs, non-encrypted reasoning and final completion remain strictly reconciled. A true comparison does **not** prove those other checks passed. These fields remain informational and never override a missing Provider receipt or authorize rejected Tools.

The older SDK could reject a properly completed stream solely because the two ciphertext strings differed. The narrow correction uses **only** completed-done encryption as the authoritative replay source; no fallback to partial added reasoning, loss of Tool identity checks or unverified external retry is allowed. An unknown or incomplete first response is not permission to retry a real Provider operation blindly. Keep the failed report, verify the upstream API's stateless encrypted-reasoning contract and use the observed structural fields to localize further investigation before another billable task run. If preflight eventually passes, the interactive **quality** command **will proceed to all 40 cases** (or more with repetitions); plan that expense beforehand. Use `probe` instead for a bounded capability-only retest.

## Observable measurements

- **Pass/fail:** deterministic evidence Oracle, actual saved draft, required Tool calls and same-Run recovery; failure stage and code retained rather than rewritten to success
- **Work:** physical Model invocation count, registered Tool executions, artifact reads, draft writes, recovered/blocked replay checks
- **Latency:** monotonic end-to-end sample duration, model-call durations, sample p50/p95; no invented queue/wall latency
- **Efficiency:** known Provider input/output tokens and coverage; group-level totals are shown only when *every* corresponding model receipt is known
- **Group coverage:** attempted/pass/skipped totals and descriptive pass percentages for the five fixed groups

Prices, Provider prompt caching charges and infrastructure cost are **not** inferable from token usage alone. Monetary cost is not estimated or silently set to zero. The 40-case corpus has no human preference judge or general ability score.

## Comparison and interpretation rules

Every report records the code revision/dirty bit, suite, task version, dataset hash, exact Provider/model/endpoint fingerprint, frozen ModelOptions, Context bounds, Tool policy, Oracle and execution profile. Numerical report comparison requires matching task version, full dataset hash, profile, output ceiling, repetitions and suite; environment/model differences are shown, not concealed. **Unfinished or interrupted reports show observed samples only, with no claimed benchmark uplift.** The same fixed task and trusted Oracle must be used for both configurations.

`quality` reports use the existing schema v1 with an **optional** `suite` member, while historical two-case reports omit it and remain readable. No compatibility bridge, SQL migration or additional runtime state is introduced. The 40-case task identity is `release-evidence-quality-v1`, distinct from the historical `release-recovery-v1`.

For a credible real comparison, use the same configuration and budget, identical corpus, at least 2–3 repetitions if budget allows, and disclose endpoint, model, operating environment and code revision. Small differences between groups of eight samples should not be treated as statistically meaningful; this report automatically declares no winner.

## Acceptance and known limits

Automatic CI must verify every fixture against the scripted real Agent path, data uniqueness and model-independent dataset hash, strict suite ID/count/repetition admission, fail-closed invalid reports, unknown Token semantics, partial sampling and legacy report compatibility. The scripted path is not a proxy for real Provider performance.

The exercise uses memory-backed simulated process interruption and a test Store clock. It cannot certify an actual process restart, external billing, production SQLite or remote unknown-effect reconciliation. Those claims require independently run live/integration gates. The two untracked local Provider probe scripts are not inputs to this suite and must remain untouched.
