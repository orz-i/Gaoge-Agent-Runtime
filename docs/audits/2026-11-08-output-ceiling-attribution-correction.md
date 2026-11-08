# 2026-11-08 — Output ceiling / input reservation / failure attribution correction

## Scope

This correction is based on the genuine private `distractor-05` pilot report
`artifacts/agent-baselines/20261108T022700Z-85cf9d6e.json`, SHA-256
`12a851e73ff4abe82a24fef91638e2629c7c4df3ef0eabce8fa73cdc8f21c9e5`.
The original JSON/Markdown reports are kept intact, not committed or reclassified.

The earlier analysis repeatedly implied that a large `maxOutputTokens` setting
could explain task failure merely because it was configured. That was an
unsupported **causal attribution**. A model's per-request output ceiling is not
the number of output tokens it must generate, nor the number it did generate.

## Observed evidence, without inventing a root cause

| Evidence class | Observed facts | Allowed conclusion |
| --- | --- | --- |
| Provider compatibility | JSON Schema probe HTTP 200, synthetic Tool/replay passed; reasoning not observed in that synthetic probe | Specific probes passed, not proof of subsequent task correctness or encrypted-reasoning coverage |
| Actual sample | `distractor-05`, 7 model calls, 7 Tool executions, 6 Artifact reads, **0 saved drafts**, 19.055 seconds | Evidence task did not reach the committed-draft interruption; no recovery path was exercised |
| Actual usage | **7,902 input / 979 output tokens total** across 7 model calls; 630 reasoning tokens included in output; 7/7 usage receipts known | These are observed cumulative task tokens, not a mandate to consume 128,000 tokens per call |
| Last observed response | HTTP 200 SSE; latest provider Tool call had `read_context_artifact(offset=98648,limit=8000)` without `query` | The last Tool request did **not** activate the candidate-search query path; cannot prove every earlier Tool call was wrong |
| Legacy failure | `task_did_not_reach_interruption`, from the source revision `33c876b` | Broad and **historically ambiguous**: neither actual terminal status nor typed cause was retained in that report. Do not infer Provider failure, Context rejection, root cause, or recovery failure |
| Execution profile | Context window **1,050,000**, Host input ceiling **32,768**, `maxOutputTokens=128000` | Input limit is determined by a separate min/cap rule, not by actual output consumption |

For the task-agent Host and this configured window, with the same ModelWindow
effective percentage and no smaller MaxContextTokens override:

```text
effective input = min(contextTokens - ReservedOutputTokens, Host MaxInputTokens)

maxOutputTokens = 128000: min(1050000 - 128000, 32768) = 32768
maxOutputTokens =   4096: min(1050000 -   4096, 32768) = 32768
```

The active Host sets `ReservedOutputTokens` from the configured output ceiling;
neither value describes **actual** output usage. Changing this particular
output ceiling from 128,000 to 4,096 would **not** increase the effective
Host input limit here. Output reserves can matter on smaller windows or when
they reduce available model input *below* the Host ceiling; the Go test
explicitly preserves this distinction. Changes to output ceilings may affect
the model's available maximum output and experiment identity, so different
settings are not numerically comparable without separate controls. None
of that is evidence that this run failed because of its output ceiling.

## Systematic correction in code

1. `scripts/run-agent-baseline.py` renders the **separate definitions**
   of output ceiling, Context reservation, computed effective input and
   actual Provider Usage. It only renders this model-window budget fact for a
   failed **task** with complete positive configuration; it never claims
   an output ceiling is consumed or is the failure root cause.
2. Broad `task_did_not_reach_interruption` guidance now explicitly rejects
   causal inference from `maxOutputTokens`. HTTP 400/422/provider failure
   advice no longer recommends arbitrarily changing the output cap;
   adjustment is only conditional on an actual upstream limit error.
3. Go tests cover the exact 128,000-vs-4,096 boundary and a counterexample
   where an enormous output reservation **does** reduce the effective input
   limit. Python tests cover typed/missing configurations, failed vs
   successful/preflight-only reports, and absence of a false output-cap
   recommendation. These are reporter/Host tests, not kernel behavior changes.
4. PR #97's already-merged Host pre-interruption classifier is included in
   the stacked development branch `c5dea3b`. It improves **future** report
   categories only; it cannot repair original missing evidence or mutate
   this pilot's immutable JSON.

## Required causal standard

Every future explanation must separate (a) raw configuration, (b) measured
Provider responses and consumed tokens, (c) Host Context admission and
reservation, (d) registered Tool/effect receipts, and (e) exact typed failure
and terminal Run/Turn state. Only evidence in the same execution path may
support a causal claim. A generic error class, a last HTTP 200 or an
unexercised optional capability cannot establish an underlying root cause.

Next investigation should target the final Tool trajectory and the actual
pre-interruption terminal/error class in a *new* run when explicitly
authorized, or deterministic reproduction of the offset-only pagination
variant. Avoid using more paid requests merely to validate a speculative
output-ceiling hypothesis.

No paid Provider request was made for this correction.
