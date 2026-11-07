# 2026-11-07 — First complete real-model Agent effect run (40 cases)

**Evidence status:** complete observation with **three failed tasks**; not an all-pass release gate. This is a **single model / single repetition / one task family** benchmark. Never rewrite the original result to 40/40 or treat offline calibration as real-model quality.

## Immutable local source and test configuration

- Raw, private JSON report (intentionally not added to Git): `artifacts/agent-baselines/20261107T093913Z-12da49b7.json`; companion Markdown stays local as well.
- SHA-256 of raw JSON (integrity only): `562c1115541379df1d027faba6f5e74078fa46cb3dbb32a1c259f0009131db03`. The JSON report was successfully reopened and checked by the repository's native `read_report()` protocol validator.
- UTC report timestamps: `2026-11-07T09:39:14Z` to `2026-11-07T09:46:38Z`.
- Source revision: `c503aca1d21308b2e3ce243d5c70f9fbc2a9047e` (merged PR #94). **Recorded source.dirty=true**. The runtime report does not attest a clean immutable tree or a signed Provider trace; do not convert this into PR-Head live CI evidence.
- Official Responses endpoint: `https://api.openai.com/v1/responses`; provider `openai`; model `gpt-5.6-terra`; reasoning effort `medium`; certified Context capacity **1,050,000** tokens; per-call output ceiling **4,096** tokens.
- Host Context **service ceiling 32,768 input tokens**; conservative estimated hard budget remains fail-closed without an exact model-aware TokenCounter. One repetition of each case, suite `evidence-40-v1`, task version `release-evidence-quality-v1`, dataset/profile hash `sha256:1d57c9a8827816d63340f027a60bfdc92c63624e982232cb4f49ba2ff2bf3993`. Legacy two-case dataset and older 8,192-ceiling profile are not numerically interchangeable with this measurement.
- Schema preflight **PASS** (HTTP 200, JSON); synthetic OpenAI Tool/replay preflight **PASS**, **two** physical/model calls, `reasoningSeen=true`, `encryptedReplaySeen=true`, complete usage. These are distinct from the actual task samples.

## Observed outcome

| Metric | Real observed value |
| --- | ---: |
| Requested / attempted / skipped | 40 / 40 / 0 |
| Passed / failed | **37 / 3** |
| Attempted-task success rate | **92.5%** |
| Summary `complete` (all requested attempted) | true |
| Overall report `status` (three failures) | **failed** |
| Physical model calls counted on task path | 174 |
| Registered tool executions | 137 |
| Task input / output tokens | 131,766 / 13,393 |
| Input cache-read tokens | 18,284 (a subset of input tokens, **not additional input**) |
| Task reasoning tokens | 4,033 (subset of output tokens) |
| Task usage receipt coverage | 174/174, none unknown |
| Sample durations total | 431,517 ms (sequential sampled task durations) |
| Sample p50 / p95 | 8,998 / 25,876 ms |
| Sample min / max | 5,557 / 27,734 ms |

Preflight token usage is **not** mixed into the task totals: unary Schema 62 input / 13 output tokens; two Tool/replay requests totaled 280 input / 49 output tokens (including 14 reasoning tokens). Monetary cost cannot be inferred solely from these receipts without verified Provider pricing/caching rules.

## Five groups — same release-evidence task family

| Group | Passed / attempted | Observed success rate | Model calls | Tool calls | Artifact reads | Input / output tokens |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| `code` | 8 / 8 | 100% | 32 | 24 | 8 | 21,355 / 2,008 |
| `window` | 8 / 8 | 100% | 32 | 24 | 8 | 21,446 / 2,056 |
| `distractor` | **5 / 8** | **62.5%** | **46** | **41** | **28** | **46,185 / 5,275** |
| `unicode` | 8 / 8 | 100% | 32 | 24 | 8 | 21,493 / 2,043 |
| `layout` | 8 / 8 | 100% | 32 | 24 | 8 | 21,287 / 2,011 |

The Distractor group incurred repeated Context Artifact reads and disproportionate calls; its 46,185 input tokens represent approximately 35% of total task input tokens for only eight of the 40 cases. Small groups of eight and one repetition do **not** establish statistical significance or a reliable population capability ranking.

## Three failures — exact observed trust boundaries

| Case | Stage / report failure | Model / Tool calls | Lookup / Artifact reads / Draft writes | Observed last Model Tool query |
| --- | --- | --- | --- | --- |
| `distractor-01` | `evidence` / `task_did_not_reach_interruption` | 6 / 6 | 1 / 5 / 0 | `deployment` |
| `distractor-05` | `evidence` / `context_budget_exceeded` | 8 / 8 | 1 / 7 / 0 | `RELEASE` |
| `distractor-07` | `evidence` / `context_budget_exceeded` | 7 / 7 | 1 / 6 / 0 | `deploymentCode` |

Evidence was extracted **only from safe, already-redacted reports**; the last request classification came from a parsed `response.completed` Tool call and did not persist artifact IDs, ciphertext, raw model instructions, original source documents or user credentials. Each failed case had a last observed HTTP 200 / `event_stream` Model response. That response may precede a **local** Context prepare rejection and does not prove that the Provider rejected the following request.

The Context Artifact query implementation performs **case-sensitive literal search** starting at an optional offset. The task prompt and tool description specifically direct exact search for `RELEASE R-17`, while the observed final queries were not that exact key. `deployment` and `deploymentCode` differ in spelling or casing from the fixture's `DEPLOYMENT_CODE`; `RELEASE` is broader than the exact target release. This provides **supporting evidence of imprecise search strategy and repeated reads**, not proof that every earlier read failed or that the model was uniquely responsible. For context, `distractor-08` eventually passed with five artifact reads, so repeated reads are a correlated burden, **not** a deterministic failure condition.

- `distractor-01`: the Agent did not reach the simulated post-draft interruption condition, and no draft was written. The wrapper `task_did_not_reach_interruption` covers more than one underlying termination/error possibility; the original report does not expose `startErr`. Do **not** claim a specific underlying model, Tool, schema or Kernel fault without additional safe evidence.
- `distractor-05` and `distractor-07`: after respectively 7 and 6 artifact reads with no saved draft, the SDK's managed Context preparation returned `context_budget_exceeded` before dispatching another request. The Host quality ceiling remained **32,768**, distinct from the stated Model Context capacity of 1,050,000. Conservative serialized-byte hard accounting, including opaque replay, remains a legitimate safety limit. There is **no evidence** of Provider HTTP rejection for the unissued next request.
- All three failed **before** the intentional post-commit interruption and recovery exercise. Their seven recovery booleans are false because that path was not reached, **not** because the system attempted recovery and then violated lease, Run/Turn identity or duplicate-effect guarantees. Successful tasks retained the actual Oracle and recovery checks.

## Control and interpretation

1. **Preserve this result.** The 40-case observation is fully sampled, with three real task failures. Do not replace it with scripted 40/40 or retrospectively change its denominator, budget, oracle, Tool search semantics, data or task completion evidence.
2. **No automatic paid rerun.** A future repetition, if explicitly authorized, needs a fresh private API key and budget. One more repetition under the **same dataset hash and profile** can help assess whether failures are model stochasticity or repeatable group-specific behavior. A changed prompt/tool contract, exact-search strategy, budget, task family or Host policy requires a new measurement identity and separate report.
3. **Do not weaken fail-closed Context/Provider rules** or silently raise the budget to convert failed examples into passes. To explore model search behavior, measure query specificity and artifact-read counts with privacy-safe observations, or prototype a separately versioned Host prompt/Tool UX experiment. The present dataset does already instruct exact `RELEASE R-17` search.
4. **Classify `task_did_not_reach_interruption` more narrowly only in a future, separately reviewed diagnostic change**, preserving a stable protocol and never serializing raw error text, Provider payloads or credentials.
5. **Limits:** single Provider model, one task family, one repetition, in-memory interruption/recovery with a simulated Store clock. No general Agent intelligence score, live process-restart guarantee, external-effect exactly-once claim, or pricing claim.

## Local offline cross-check

The same merged checkout independently passed the Go tests `TestQualitySuiteAllFortyCasesPassOfflineExecutionAndRecoveryOracle`, `TestQualitySuiteManifestIsDistinctDeterministicAndModelIndependent`, and `TestQualityServiceContextCapKeepsLegacyHashAndModelCapacityFence`. This verifies fixture and Oracle mechanics only, not the 37/40 real-model success count.

The original private JSON and Markdown reports remain on the Mac mini and are **not** part of this source-controlled evidence note.
