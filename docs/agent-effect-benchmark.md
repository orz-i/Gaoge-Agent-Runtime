# Agent effect benchmark — evidence-quality v1

**Status:** opt-in beta.24 development suite (2026-11-07). Not a general Agent benchmark, leaderboard, or release gate. A first full official OpenAI real-model run has now **attempted all 40 cases**, with **37 passed / 3 failed** (92.5% of attempted samples, overall report status `failed`). This is a single run of one evidence task family, not a general-model score. See [first complete run audit](audits/2026-11-07-first-real-agent-quality-40.md). This historical result predates the later candidate Tool and native input-counting contracts; it is not evidence of improved performance under those profiles.

## Third official one-case pilot — native count passed, Tool budget exhausted (2026-11-08)

On clean `43f42d6` under the original v2 manifest
`sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`,
the official `gpt-5.6-terra` `distractor-05` pilot observed native
input counting **12/12 accepted**, with **11/11 exact same-input
comparisons equal to completed Provider input-token Usage**. This
establishes metering agreement for the eleven matched calls on that
model/endpoint, not global compatibility or cost. The single task
**failed**: `agent.tool_limit`, one lookup plus nine Artifact reads,
zero drafts; all ten authorized Tools were spent before the required
draft. The last attempted queryless `offset=0` read was not executed,
and no Content/Context overrun was observed. Schema preflight passed;
the two-call Tool probe did not observe encrypted reasoning replay
(`reasoning=false, encrypted=false`), so no such claim is made for
that probe. The original report is immutable (SHA-256
`a581b069d9659547716ebe9bfdbfc639b71ffad4b74de2e4a9fa90a73352f49e`).

The **quality-only** navigation guidance now prioritizes the complete
target identifier already stated in the user's task, avoids broad
queries and offset-zero loops, and saves a draft immediately after an
authoritative exact-original-source read; candidate/normalized hints
remain suggestions and are re-read if needed. Exactly **10** SDK Tool
calls remain the hard maximum, including the draft. No facts,
authorization, Tool schema, Context policy, source corpus or Oracle
were changed. A source-blind scripted Agent with the full 40-case
corpus demonstrates one lookup, one exact original read and one draft
per case, preserving same-Run recovery and leaving seven unused
Tool slots. This is **not** a new successful real-model result.

The changed evaluation instructions necessarily rotate the quality
manifest: OpenAI
`sha256:9e6a18425ab3ce620e9b65d864626855f60dcd860533741154fc29c364f9f114`,
scripted calibration
`sha256:9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`.
Keep the historical v2 real failure at its original dataset hash;
do not numerically compare different profiles as the same task.
See the [full real-model forensic and corrective review](audits/2026-11-08-real-distractor05-metering-passed-tool-limit.md).

## Successful official real-model target pilot — 2026-11-08

The current quality Host profile
`sha256:9e6a18425ab3ce620e9b65d864626855f60dcd860533741154fc29c364f9f114`
has now completed a **successful official OpenAI `gpt-5.6-terra`
`distractor-05` single-case pilot**. Clean source `b219b83`;
private report `20261108T073605Z-97c7b1ea.json`
(SHA-256 `228785b209571def2cf67e50d541eadbb9b36edc3fe1070714c19f7399047f03`)
was validated but not committed or edited.

Actual task work: four model calls, exactly **three committed
Tools** (one lookup, one exact query-backed original-source
Artifact read and one saved draft), passed draft Oracle, legitimate
interruption/lease fencing/same Run and Turn recovery and no Tool
replay. The optional native Count endpoint accepted 5/5 counting
attempts; 4/4 **same-input** count-to-completed-Usage comparisons
were exact, with zero observed low/high counts. One unmatched counting
attempt remains unverified. The strict Schema and two-step
stateless Tool probe passed, and **both reasoning and encrypted
replay were observed in this run**.

This was **one attempted case out of forty**, with 39 untested
cases, and does **not** establish general Agent ability, a new full
40-case benchmark score, or success with all Providers. Earlier
failed pilots and the original descriptive 37/40 measurement
retain their original distinct hashes/statuses. See
[official real evidence and exit scope](audits/2026-11-08-official-real-distractor05-success.md).

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

## First real Agent task: bounded one-case pilot

After Provider preflight succeeds, verify the **actual Agent/Tool/Context/Oracle/recovery path** before committing to the entire 40-case paid suite. The pilot is a separate, explicit command:

```sh
make agent-quality-pilot
# Or reuse an existing report's public endpoint/model/output budget:
python3 scripts/run-agent-baseline.py pilot artifacts/agent-baselines/<report>.json
```

The Host requires a newly hidden API key, trusted model capability profile, and interactive opt-in. The Go runner restricts this mode to **`evidence-40-v1`, repetitions=1**, and runs precisely **`code-01` once** after the usual strict Schema and synthetic Tool/replay preflight. It cannot start any of the remaining 39 cases, even if the first passes or fails. The same real Agent Loop, existing deterministic evidence Oracle and same-Run recovery tests are exercised; the task may invoke the model multiple times (existing cap of 12 Model calls, 10 Tools, and 5-minute sample timeout), in addition to the 1–3 preflight calls. These are upper bounds, **not** monetary price guarantees. A successful fake/local Model is not a substitute for a real Provider observation.

The report retains the same **40-case source corpus and task version** and records its actual measurement profile/dataset hash, plus `pilotOnly=true` and `pilotCaseID=code-01`. The quality suite's Context-budget profile changed after the first real pilot; historical reports keep their own earlier hash and must not be numerically compared with the new profile. On task completion it reports `attempted=1`, `skipped=39`, `complete=false` and either `pilot_sample_passed` or `pilot_sample_failed`; a failed preflight reports attempted=0. The attempted single-case outcome, latency and known/unknown token receipts are observations, **not a 40-case success rate, grouped score, or leaderboard**. Numeric comparison with full or other pilot reports is explicitly disallowed. `retry` refuses pilot reports to avoid launching all 40 tasks; use another explicit `pilot` command to repeat the pilot, or `quality` only when the full budget has been approved.

**First real pilot observation (2026-11-07):** the official OpenAI Responses `gpt-5.6-terra` trial passed the Schema/Tool replay preflight, then executed `lookup_release` and `read_context_artifact` before failing locally at `context_budget_exceeded`; it issued 2 task Model calls and 2 Tool calls, with 0 draft writes and 39 tasks skipped. The prior **8192-token Host service ceiling**, not the 1,050,000-token model capacity, was the effective constraint. Without an exact TokenCounter, the Context manager conservatively bounds serialized bytes, including encrypted replay. A synthetic 12 KB encrypted reasoning completion reproduced the same local failure; the quality suite's Host limit was raised to **32768** while retaining the original **8192** for the historical two-case suite, with a new quality dataset hash and no SDK estimator bypass. The actual original encrypted payload length is unavailable from the redacted report, and the updated code has **not** been retested against the paid model. See [first-pilot evidence and limitations](audits/2026-11-07-first-live-agent-pilot.md).

**Second real pilot observation (2026-11-07):** After the quality-only Host cap adjustment, the same `gpt-5.6-terra` / medium-effort `code-01` trial passed its full Agent Oracle and all recovery checks. Local report `20261107T065511Z-44b17e65.json` was validated as `pilot_sample_passed`, with 4 Model calls, 3 Tool executions (including exactly one draft), 9,990 ms task duration, complete 4/4 task Usage receipts (2,709 input / 268 output Tokens), and **39/40 tasks unattempted**. Its corrected quality dataset hash is `sha256:1d57c9a8827816d63340f027a60bfdc92c63624e982232cb4f49ba2ff2bf3993`, while `source.dirty=true` means the result is not yet a locked-commit acceptance. The synthetic Tool preflight was also `passed` but reported `reasoningSeen=false` and `encryptedReplaySeen=false`: this particular probe proved ordinary Tool continuation, **not** encrypted reasoning replay coverage. A separate earlier Provider preflight had verified encrypted reasoning replay. See the [live pilot audit](audits/2026-11-07-first-live-agent-pilot.md) for the distinct evidence claims.

**Go/no-go after the pilot:** inspect the real oracle verdict, Model/Tool call counts, provider Usage coverage, saved draft, Context Artifact read, lease fencing and same-Run recovery evidence. A single successful case is a *smoke check*, not evidence for cross-model or cross-workload performance. If it fails, diagnose first rather than automatically proceeding to 40 samples. Any full real-model benchmark requires separate explicit budget authorization and the regular `quality` command. Wider coding, browser and research task families require independently designed Oracles; do not extend this one release-evidence family by merely cloning more cases.

## First complete 40-case real-model measurement — 2026-11-07

The first full `evidence-40-v1` run using official OpenAI Responses, `gpt-5.6-terra`, medium reasoning, output cap 4,096 and one repetition per case finished with **40 attempted, 37 passed, 3 failed, none skipped**; all 174 task Model invocations had known usage. The group breakdown was **code 8/8, window 8/8, distractor 5/8, unicode 8/8, layout 8/8**. The task subtotal was 131,766 input Tokens (including 18,284 cached input Tokens), 13,393 output Tokens, 174 Model calls, 137 Tool executions, and p50/p95 sample durations of 8,998/25,876 milliseconds. The separate Schema and encrypted Tool/replay preflight passed. **Complete** in this report means all requested samples were *attempted*, not that they all passed; the overall result remains `failed`.

All three failures were in the distractor group: `distractor-01` never reached the intentional post-draft interruption after five Context Artifact reads, while `distractor-05` and `distractor-07` each hit the local `context_budget_exceeded` safety boundary after seven and six artifact reads, respectively. The final observable artifact queries were less specific than the prompted exact `RELEASE R-17` literal. This is evidence of variable search behavior, not proof of a Provider defect; do not disable Context limits or treat the missing recovery checks as failed recovery attempts. The original local report (`20261107T093913Z-12da49b7.json`, source marked dirty) is preserved and not committed. For the full immutable profile, per-group usage, failure trace limits, SHA-256 and interpretation rules, see the [dated audit record](audits/2026-11-07-first-real-agent-quality-40.md).

This is a **descriptive first measurement**, not a leaderboard or a claim about coding, browser, research or production crash-recovery abilities. Do not rerun or alter the dataset to pursue a 40/40 score without separately authorizing paid calls and creating a distinct measurement identity whenever execution policy or task semantics change.

## Targeted ambiguous-evidence live pilot (opt-in, bounded)

On this branch, the original 40-case manifest adds a *second*, distinct, Host-selected pilot of `distractor-05`, the historically failing ambiguous-evidence case:

```sh
make agent-quality-distractor-pilot
# Or reuse a validated report's public configuration; a new hidden key is mandatory:
python3 scripts/run-agent-baseline.py pilot-distractor artifacts/agent-baselines/<report>.json
```

This mode first runs the same strict Schema + (for OpenAI) Tool/replay compatibility preflight, then launches **at most one** `distractor-05` Agent task with the real Tool, Context, draft Oracle and recovery checks. It cannot be activated by untrusted stdin, a model prompt, or a report field. The existing `pilot` continues to run `code-01` only. The new pilot emits `requested=40, attempted<=1, skipped>=39, complete=false`, with an explicit `pilotCaseID`. Neither pilot is a 40-case result; automatic replay of pilot reports as a full 40-case task is prohibited. The changed native-count strategy is included in a **new OpenAI quality dataset hash**; do not compare success rates numerically with the old 37/40 profile.

For the `evidence-40-v1` OpenAI Host, `runtimecontext.Manager` is optionally wired to `provider.ContextTokenCounter`. When the routed, frozen model request matches the certified OpenAI Responses client, a bounded read-only `POST /responses/input_tokens` is sent to the configured same-origin endpoint, reusing the native Provider input/tool/replay projection. A positive native count can inform Context admission before the ModelInvocation is committed. Endpoint refusal, malformed counts, a model/protocol mismatch or unimplemented proxy support retain the **original conservative hard-byte fallback**. The legacy two-case suite remains unchanged; the Anthropic counting endpoint is documented as an estimate and does not authorize a tight hard budget.

Input-count HTTP requests are extra pre-generation calls and can add time, network traffic or provider-specific quotas. They are **not** model generations or known billing Usage, and the task Model call count does not include them. No native-count latency/coverage telemetry has yet been claimed for a live run. To accept this candidate: record whether the count endpoint was reached and accepted for each planned request, validate the provider's real token count against the original completed Usage when available, and report cost/latency separately. No paid API request was executed in the development branch's deterministic tests.

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

## Output ceiling is not output usage or a root cause

`maxOutputTokens` is the **maximum output allowed per request**, not a
required generation size. The task Host separately maps this ceiling to
`ModelWindow.ReservedOutputTokens` for conservative input admission; this
does not mean the model consumed the reservation. For the 2026-11-08
`gpt-5.6-terra` `distractor-05` pilot, both
`min(1,050,000-128,000, 32,768)` and
`min(1,050,000-4,096, 32,768)` are **32,768**. Therefore the 128,000
output ceiling cannot, by itself, explain that run's early termination.
Its actual measured total was 979 output tokens across 7 model calls
(630 of these were reasoning tokens); its historical
`task_did_not_reach_interruption` code is ambiguous and must not be
retroactively relabeled. Context budget rejection requires its own
typed observation, while failure to save a draft requires actual
Tool/Run evidence. See the
[causal-attribution correction](audits/2026-11-08-output-ceiling-attribution-correction.md).
Changing output ceilings creates another experimental configuration,
not proof of improvement or a generic remedy for Tool retrieval failures.

## Long-artifact offset paging and evidence-strategy observations

The new bounded queryless candidate tool profile (`harness-candidate-search-v2`)
does **not** force an answer or silently substitute a search. When the Host
opts into the candidate reader and a large archived artifact is read with
`offset` but no `query`, the tool now returns an *exact bounded source
prefix*, a matching forward-only Unicode-rune `nextOffset`, and a fixed,
non-evidentiary `searchHint`. The complete result fits **512 bytes**
even if the model requests `limit=8000`, preventing the important hint
from being immediately archived with the oversized Tool response.
Smaller documents, query-backed reads, and the ordinary literal reader
remain unchanged. A direct source read is still mandatory for evidence.

Quality sample reports may optionally include `artifactReadStrategy`
with six **counts only**: queried reads, offset-only reads, candidate
pages, normalized hint pages, hinted offset pages and terminal pages.
The Host observes successful registered Tool handler calls, not an LLM
claim. No original query text, cursor, artifact ID or preview is
serialized into this summary. The Python reader validates count totals
and subset relations, accepts historical reports without this field,
and does not interpret these metrics as a root cause.

The official `gpt-5.6-terra` `distractor-05` report
`20261108T022700Z-85cf9d6e.json` remains **failed** on the original
`33c876b` source; its last captured response showed an offset-only
read and its historic `task_did_not_reach_interruption` failure is still
ambiguous. The new Source-blind scripted Agent demonstrates that
`offset=0,limit=8000` can be followed by a Tool-provided hint, real
candidate search, exact source reread, one draft and same-Run recovery.
This does not establish any real-model improvement. The OpenAI
quality dataset hash is now
`sha256:7905b6abca186865869bcdc89ad8178cc2cb2ec4b5b1ada64aea4356ec97f992`,
different from the prior count-aware
`sha256:d22c61a16559e72d0c75c8f61ba342ac16c9382d3a55afc6a5622d8d4f70624b`;
never rank or compare these profiles numerically as the same workload.
See the [queryless navigation governance record](audits/2026-11-08-context-queryless-paging-advice.md).

## Trusted Agent terminal evidence after the real second pilot

The 2026-11-08 `gpt-5.6-terra` `distractor-05` real pilot at
`d674b86` (immutable report `20261108T033222Z-4271a907.json`)
returned `task_failed_before_interruption` after 8 model calls and
8 Tool executions, including 7 Artifact reads and no draft. The
final captured Tool call identity matches the eighth recorded Tool
execution receipt. The model had used enhanced candidate search,
but the report did not persist a trusted Harness Turn terminal code.
Do not claim the last Tool failed, the model exhausted its limits,
or that a native counting error caused the run to terminate.

Subsequent reports optionally include `preInterruption.turnStatus`
and an exact whitelist `agentErrorCode`, drawn from the original
Harness Turn after `Prompt` failed to reach the expected draft
interruption. The API excludes arbitrary `Turn.ErrorDetail` and
unknown Provider-controlled error strings. The report validator
rejects unknown status/code combinations and keeps old reports
unchanged rather than inventing their missing terminal facts.
See the [real pilot forensic review](audits/2026-11-08-real-distractor05-terminal-evidence.md).

## Exact signed replay in native input counting

A real `gpt-5.6-terra` pilot observed 12 native input-counter calls but only
3 accepted results; the original report does not contain the nine individual
failure causes. A new **offline full-path** Context→Router→Provider test found
one concrete defect capable of causing that pattern: canonicalizing nested
Tool Call argument JSON rearranged keys, while encrypted reasoning/Tool
replay retained the Provider's original argument string. The byte-strict
replay check then correctly rejected native counting before HTTP.

The corrected optional count path serializes the original typed
`model.Request` using a single shared `MarshalRequestForInputCount`
helper without generic reordering or HTML escaping, then validates an exact typed
round trip in the Provider bridge. The Context hard-byte bound is strengthened to the larger of normalized
and original typed request sizes whenever this native counter is configured;
strict signed replay equality remains intact. Valid signed Tool/reasoning
replay now reaches native count HTTP in loopback tests; tampered/duplicate
arguments still fall back conservatively without an HTTP count request.
This is not a real-Provider verification or proof of a changed task outcome.

The v2 OpenAI native counting strategy rotates the quality dataset hash to
`sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`,
from `sha256:7905b6abca186865869bcdc89ad8178cc2cb2ec4b5b1ada64aea4356ec97f992`.
Do not numerically compare prior paid pilots as the same profile. Neither
the scripted 40-case workload nor the historical two-case baseline is
changed. See [native count replay audit](audits/2026-11-08-native-count-replay-order-hardcut.md).

## Native input-counting observations

The quality report's optional `inputCountObservations.unavailableReasons`
partitions unavailable native count attempts into six fixed categories:
`localRejected`, `http4xx`, `http5xx`, `otherHTTP`,
`transportOrProtocol` and `other`. These originate from trusted
Provider error types, never HTTP body/message text. An HTTP class
describes a typed response status observed by the selected adapter,
not a separate network receipt, billing event or model generation.
The categories sum exactly to `unavailable` and are omitted from
historical uninstrumented reports.

The optional `inputCountComparison` is an **actual native count vs.
Provider Usage evidence check**, not another Token count request.
Only accepted counts for the exact same input-affecting request, followed
by a successful model response with known `usage.inputTokens`, may be
compared. It exports only `compared`, `equal`, `belowUsage`,
`aboveUsage` and `maxUnderestimate`; the private correlation digest,
replay and message content never leave the process. Run identity and
the output-only Token ceiling do not affect this comparison, but source
messages, model, Tool schemas/arguments, output Schema and reasoning
effort must remain unchanged. Any `belowUsage` is a metering safety
blocker, and an unmatched/unknown request is **never** an equality.
These diagnostics are report-only and do not rotate the current v2
dataset hash or rewrite either genuine failed pilot. See the
[real-model verification gate](audits/2026-11-08-real-model-count-cause-and-usage-verification.md).

For the OpenAI `evidence-40-v1` Host, a sample may include the optional
`inputCountObservations` object with **attempts**, **accepted** and
**unavailable** counts. This is measured around each call into the already
configured native Provider counter, before Context applies its own admission
policy; a positive response is counted as accepted, other responses as
unavailable. The object is not Provider Usage, not the number of outbound HTTP
requests and not proof of a billable generation. An unavailable count does not
authorize a tighter Context budget: the existing conservative byte upper bound
remains in effect. Even an accepted count does not prove that the model
subsequently dispatched a generation. The existing `modelCalls` and Usage
receipts remain separate.

The Python validator enforces exact three-field shapes, nonnegative integers
and `accepted + unavailable == attempts`, and rejects this OpenAI quality-only
field for other protocols or the old two-case baseline. Historical reports do
not gain fabricated observations. This is **report-only instrumentation**;
the original Model request, Tool schema, Oracle, Context budget, manifest and
quality dataset hash do not change. Actual transport-specific counting
latency/usage and any live improvement remain unverified until a new authorized
provider run. See the [verification audit](audits/2026-11-08-provider-context-identity-and-observations.md).

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
