# 2026-11-08 — Provider/Context real-model verification: count failure origin and exact-input-to-Usage evidence

## Scope and immutable baseline

- PR: **#98**, based on `main` after merged PRs #95, #96 and #97.
- The latest previous genuine `gpt-5.6-terra` `distractor-05` pilot is still
  **`pilot_sample_failed`**, source `d674b86`, report
  `20261108T033222Z-4271a907.json`, SHA-256
  `68c344a0cafce6dba8f005de4e7445dc520d9ed73b3c688f656c50150e36d768`.
  It observed 8 model calls, 8 Tool executions, 7 Artifact reads (4 queried,
  3 offset-only), no draft, and 12 native count attempts (3 accepted, 9
  unavailable). Its last captured read was **executed**, not rejected.
  The actual terminal cause is not recoverable from that immutable report.
- Earlier original paid pilot
  `20261108T022700Z-85cf9d6e.json` SHA-256
  `12a851e73ff4abe82a24fef91638e2629c7c4df3ef0eabce8fa73cdc8f21c9e5`
  also remains a failure, not a recovered pass.
- Historical complete 40-case score **37/40** on the earlier profile was
  descriptive and cannot be compared as an identical workload against the
  current replay-safe native-count v2 profile
  `sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`.
  The scripted 40/40 is infrastructure/Oracle calibration **only**.

The native `POST /v1/responses/input_tokens` endpoint is documented by
OpenAI as an exact input-token count for the corresponding Responses payload,
including tools and formatting tokens:
https://developers.openai.com/api/docs/guides/token-counting

This does **not** establish compatibility with the user's actual configured
endpoint/model until a genuine request is observed. No key or paid call was
available in the connected noninteractive Mac Pi shell during this audit.

## Security and evaluation invariants

| Boundary | Invariant | Verification |
| --- | --- | --- |
| Immutable typed request → Context → Router | Signed Tool arguments, encrypted reasoning replay, Tool schema, model and forced-Tool policy are preserved exactly; count cannot grant execution | Existing v2 replay matrix, Provider counter and Source/Run scope tests |
| Native count failure → Host diagnostics | No raw Provider/HTTP response body, credentials, endpoint, Tool payload or source text is reported; error classes derived from trusted Go error types only | 4xx/5xx/redirect/invalid JSON/missing/zero response, local identity/effort rejection, cancellation; exact allowlist and malformed JSON protocol tests |
| Native counter → Context fallback | Rejected or unavailable count uses the conservative hard-byte upper bound; never accepts an unverified zero or stale prior result | Existing hard-byte and replay tests; successful sample with unsupported counter |
| Native count → completed model Usage | Compare only if the same **Provider-input-affecting** ModelRequest fields were counted, the Model step completed without error and Provider `usage.inputTokens` was accepted. Ignore Host Run/Invocation IDs and the **output-only ceiling** in the private in-memory correlation; preserve message, replay, model, Tool schema, output contract and reasoning effort | Pure matching property matrix and real Agent/Context HTTP fixture |
| Comparison → public sample | Publish only `compared`, `equal`, `belowUsage`, `aboveUsage`, `maxUnderestimate`, never the digest/request/source/ciphertext | Strict Python protocol rejects extra keys, negative/Boolean/fractional, inconsistent totals and false claims of known Usage |
| Effect recovery | Only the committed draft+interruption/lease/same-Run checks prove successful Agent recovery; last HTTP 200, count acceptance or Model output ceiling are not sufficient | Existing typed failure matrix, draft/Oracle and recovery suite |
| Comparability | This is **new optional diagnostic evidence**, not a new generation/Context/admission policy or changed task input. Old reports do not get fabricated fields and `evidence-40-v1` v2 dataset hash stays fixed | Source identity, old report SHAs and pinned dataset test |

The count failure classes are `localRejected`, `http4xx`, `http5xx`,
`otherHTTP`, `transportOrProtocol` and `other`.
A local rejection means the Host's routed/canonical/capability check did
not reach native count transport. The HTTP categories reflect a **typed
response status provided by the trusted selected adapter**, not an
independent proof of a physical request, or a model generation/billable
transaction. Transport/protocol is deliberately ambiguous; it does **not**
prove a response was received. A canceled request is not classified as
an upstream HTTP failure. The categories must partition `unavailable`
exactly and are absent from uninstrumented historical reports.

The count/Usage comparison is deliberately observational: it retains
at most a bounded small task's exact input-request SHA-256 keys **only
in process memory**, not in persisted Kernel/Host state or JSON. Each
positive count is **single-use**: its private key is consumed by the
next matching physical model invocation even if that attempt fails
or returns missing Usage, so later identical requests cannot inherit
a stale count proof. Its `compared` calls must partition into
equal/below/above; comparisons can never exceed either accepted native
counts or known-Usage model calls. It does not generate extra count
requests. A positive `belowUsage` count is an **input accounting
safety blocker**, even if a separate evidence task Oracle passes.
An unmatched call is **unknown**, never an equal match. A synthetic
loopback counter deliberately uses byte-size in place of Tokens and
correctly produces `aboveUsage` instead of false equality. Rejected
count requests yield zero matched comparisons.

## RED/GREEN matrix

- Before this pass, `inputCountObservations` only exposed three totals
  and had no safe explanation for `unavailable` or any count-to-Usage
  matching. Protocol tests accepted these historical totals but could not
  validate the new evidence.
- Provider fixed-cause tests cover local-only refusal, HTTP 400/404/429,
  HTTP 503, redirect, bad JSON/zero count and cancellation. The classified
  error retains `errors.Is(err, ErrTokenCountUnavailable)` and its public
  message never copies untrusted upstream fields.
- End-to-end one-case task fixtures exercise native 422 and 503 rejects,
  invalid success JSON, positive accepted counts and the actual Agent,
  Context, Tool and Usage pipeline. Unavailable reasons partition exactly;
  count rejection does not mint agreement or a new Tool permission.
- A first attempt to correlate the **entire** model.Request identity
  returned zero matches on the real HTTP task fixture even with positive
  accepted counts: RunID/InvocationID can change between Context counting
  and Model execution. The corrected private key ignores only Host
  metadata/output-only controls and retains all model-input-affecting
  fields. The same fixture then matches native count to known Usage.
- Exact-request property variants reject different models, source
  content, reasoning effort, Tool arguments/key order/schema, output
  contracts and mandatory Tool choice. Pure unit tests verify equal,
  undercount, overcount, missing Usage, model errors and uncorrelated
  input without any network. The independent second pass found and
  fixed **stale-count reuse**: a later identical Model request after a
  completed call, failed call or missing Usage cannot reuse the same
  prior accepted count. The Python protocol also rejects a report whose
  compared calls exceed its accepted counter observations.
- Strict Python report tests accept both old reports without optional
  diagnostics and new typed summaries; extra untrusted strings and forged
  partitions cannot enter published evidence.

## Real verification gate and environment blocker

The connected Mac Pi command environment has `stdin.isatty()=false`,
`stderr.isatty()=false`, no `OPENAI_API_KEY` injected and no
`GAOGE_MODEL_PROFILES_FILE` exported. The trusted existing
`~/.config/gaoge-agent-runtime/model-profiles.gpt-5.6-terra.json`
does contain the configured model with high effort, strict JSON Schema and
forced Tool choice, but the runner **requires hidden TTY key input**.
The audit has not searched a Keychain, persisted secrets or launched
paid requests from a noninteractive shell. Do not disable the hidden-key
requirement to force a result.

Once an operator runs the bounded pilot in a local interactive Terminal,
reuse the last paid report's *validated public configuration* (same
provider, model, effort, context capacity and output ceiling), but produce
a **new** report under the locked v2 source:

```bash
cd /Users/mini/Coding/Gaoge-Agent-Runtime-worktrees/provider-context-evidence-20261107
export GAOGE_MODEL_PROFILES_FILE="$HOME/.config/gaoge-agent-runtime/model-profiles.gpt-5.6-terra.json"
python3 scripts/run-agent-baseline.py pilot-distractor \
  artifacts/agent-baselines/20261108T033222Z-4271a907.json
```

This runs strict preflight and *at most one* real `distractor-05` task,
not a 40-case score. It may incur external charges, and the API Key is
entered only through the Terminal's hidden prompt. A new Model/Context
counting result must be evaluated with all of these separate conditions:

1. `source.dirty=false` and the correct locked source HEAD and v2
   dataset hash; exact attempted=1, skipped=39; preflight passed with
   explicitly observed reasoning/replay where claimed.
2. Native Count HTTP response acceptance/error classes as observed,
   not inferred from Model calls. At least one `compared` call and
   `belowUsage=0` / `aboveUsage=0` demonstrate **observed**
   equality; to establish coverage for the *entire* task, comparisons
   must equal the task's `usage.knownCalls` and `missingCalls=0`.
   Any absent coverage or mismatch remains a count-verification blocker.
3. For complex retrieval: bounded authorized candidate navigation,
   exact target source reread, exactly one saved draft, original Tool
   receipts, successful interruption, lease fencing, same Run/Turn and
   valid Oracle. If the task fails, report its trusted terminal code,
   not a guessed Provider/Context cause.
4. Keep cost/usage/latency separate from preflight and native count
   calls. The old v1 failed trials are never rewritten or declared
   comparable full-score benchmarks.

## Delivery status

The safe count-error class and count/Usage comparison are **Host/report
instrumentation only**, without new Model/Tool actions or altered Context
admission. The real-model v2 gate remains **BLOCKED until a genuine
operator-side execution**. Code integration tests, independent review and
actual PR-Head GitHub CI/CodeQL must still be evaluated at the final commit;
do not reuse the previous green #98 Head as that proof. GitHub's workflow
skips OTel Collector E2E, which is a separately disclosed missing check.
