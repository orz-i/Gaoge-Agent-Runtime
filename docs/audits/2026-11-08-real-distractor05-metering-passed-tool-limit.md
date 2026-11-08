# 2026-11-08 — Real distractor-05: native count verified, Tool limit reached

## Immutable genuine-model evidence

The one-case GPT-5.6 Terra `distractor-05` report was created at
`20261108T065436Z-131437a3.json`. SHA-256:
`a581b069d9659547716ebe9bfdbfc639b71ffad4b74de2e4a9fa90a73352f49e`.
It is preserved byte-for-byte; its original source is clean Head
`43f42d6471f6a0e6d17fdb317ce63e16121875ab` and quality manifest
`sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`.
The trial requested 40 cases but **attempted only one** with 39 skipped.
It is a failed single-case pilot, **not** a full 40-case score.

The native exact input counter was invoked 12 times, accepted 12, and had
zero unavailable. The new matched-input comparison covered **all 11
successful model invocations with complete Provider Usage**: 11 equal,
zero below, zero above, maximum observed underestimate zero. This is
positive evidence for actual native input metering accuracy **for the
11 compared invocations and this configured model/endpoint**; it does
not establish all possible models, caching effects, costs, or future
requests. One count attempt has no corresponding completed model-Usage
comparison; it must not be called accurate or billed.

The model made 11 calls, and the Agent executed the hard limit of **10
Tools**: one `lookup_release`, nine `read_context_artifact`, **zero
`save_draft`**. The nine reads were five query-backed, four offset-only,
with four candidate pages, one normalized navigation-hint page, and four
queryless hint pages. The original Harness Turn status is `failed`
with fixed Agent-owned error code **`agent.tool_limit`**.
The final captured HTTP 200 SSE response attempted another
`read_context_artifact` at `offset=0, limit=128` **without query**;
its CallID does not match any of the ten recorded executed Tool
receipts. Thus the **11th requested Tool did not execute**; the
Agent rejected it under the configured hard limit. The task had not
saved a draft. These facts are not a Context-token-budget failure.

Strict JSON Schema preflight passed (HTTP 200). The Tool-continuation
preflight passed with 2 model and 2 HTTP calls, but
`reasoningSeen=false`, `encryptedReplaySeen=false` **in this
particular probe**. Do not claim that this live probe demonstrated
encrypted replay; other earlier pilot observations cannot be silently
substituted for this report.

`maxOutputTokens=128000` remains a **per-call cap**, not 128k Tokens
consumed. Under this Host policy both 128k and 4096 output
reservations yield the same effective input ceiling of 32768 for the
1,050,000-token model capacity. Changing the ceiling cannot be claimed
as a fix for this authenticated Tool-limit failure.

## Trust boundaries and invariant matrix

| Boundary | Invariant | Evidence |
| --- | --- | --- |
| Provider → Context exact counting | Exact accepted counts belong only to their matched model input; no stale re-use, leakage or false comparison | Genuine 12/12 accepted, 11/11 exact count-to-Usage equality under v2; one unmatched attempt remains unverified |
| Model → Agent Tool queue | All Tool usage includes lookup, reads **and** draft; exceeding the frozen 10-call cap must fail, never silently execute an 11th Tool | 10 completed Tool receipts; final captured unexecuted queryless call; original `agent.tool_limit` |
| Agent Tool → Artifact | Authorization, Run/Turn/Session lineage, exact original source and UTF-8 offsets must remain unchanged | Existing candidate/foreign-session/invalid-artifact/Unicode/fuzz matrix; no new permission or source transformation |
| Artifact previews → accepted facts | Normalized/candidate hints are navigation only. The requested record must be read from original source before writing a draft | Existing Run-scoped source-blind 24 candidate tests and new exact-ID 40-case source-blind tests |
| Host guidance → source task | Guide extraction of the full **target identifier already in the user's request**, never the target code or window. Do not inject facts or alter the Oracle | Quality instructions revised; legacy two-case instructions unchanged, answer Schema/Oracle unchanged |
| Budget guidance → hard SDK limits | Do not relax MaxLLMCalls=12 / MaxToolCalls=10 to make a benchmark pass, invent a save receipt or authorize another tool | Hard-limit tests and explicit quality instruction regression |
| Benchmark profile → claims | Any source instruction/Tool contract change rotates quality hash; original genuine failures are not re-scored or directly compared as identical profiles | OpenAI new quality hash below; original paid report SHA remains immutable; unchanged legacy hash |

## Bounded remediation

**The verified local deficiency is a contradictory task-navigation hint:**
the quality Host previously encouraged a *short query* on archives with
similar records, even though the user supplied a unique target record
identifier `RELEASE R-17`. Real model behavior showed frequent broad
and queryless repeated reads before running out of Tools; we do not
assert that this particular instruction alone caused all reads.
The revised **quality Host only** instruction prioritizes:

1. Query the complete distinctive target identifier the user already
   provided, with `offset=0,limit=128`, before broad or sequential scans.
2. When `found=true` and the returned **original content** contains
   the complete target record and required fields, `save_draft` next.
   The original Tool's unique literal hit is itself exact source,
   not a candidate-only result; do not waste a second reread.
3. On miss or candidate-only navigation, inspect bounded previews, then
   explicitly reread the chosen exact original offset before using facts.
   A `normalized_hints` preview never becomes evidence.
4. Reserve **one of the same 10 hard Tool calls** for `save_draft`;
   don't repeat ineffective broad queries or the same offset.
   If evidence is missing, fail safely rather than fabricate a draft.

The change does **not** mutate the SDK's Agent limits, Kernel/Session,
Context budget, Tool schema, persisted receipts, encrypted reasoning
replay, output ceiling, original source facts or Oracle.

## RED/GREEN source-blind regression

The previous real model run was **RED** for task completion:
`agent.tool_limit`, 9 Artifact reads, zero draft.

Under the revised instructions, the **source-blind scripted model**
drives the **real Agent/Context/Tool/Store/Run recovery** across all 40
case variations. Every case uses exactly three committed Tool calls
(one lookup, one exact original-source read, one draft) and four model
invocations. It passes original fact Oracle, interruption, lease fence,
same Run/Turn and Tool nonreplay checks, leaving seven Tool calls spare.
The model reads actual Tool-returned source facts; source code and
deployment windows are **not** injected into quality instructions.
Independent broader/normalized candidate navigation tests remain
necessary and are retained.

These synthetic mechanics and clear instructions **do not establish
genuine GPT-5.6 Terra compliance or success**. A fresh single-case live
run is required. The new quality instruction rotates *both* pinned
manifest identities:

- OpenAI quality v2 + clarified navigation:
  `sha256:9e6a18425ab3ce620e9b65d864626855f60dcd860533741154fc29c364f9f114`
- Scripted offline quality calibration:
  `sha256:9817632b1e3a2354db4e56314af4cd79841aa7efa7f74ca2207b59792bad246a`
- The historical two-case and the previous paid quality profile
  `sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`
  remain distinct and unchanged in their original reports.

The report launcher now has a **trusted, specific** next-step message
for failed samples sharing the original fixed `agent.tool_limit` code,
rather than advising that the failure is entirely unclassified.
Historical samples without this code keep the old broad diagnostic.
The failureCode and actual Agent state are not rewritten.

## Remaining acceptance barriers

This remediation has not been tried on a fresh official paid
`distractor-05` run. The connected Mac Pi shell is noninteractive and
has no injected API Key; the existing launcher rightly requires
hidden local Terminal input. There is **no authorization to silently
retrieve/store credentials or to claim a new paid outcome**.

The next bounded real pilot must present clean locked Head, the **new**
manifest hash, one attempted case and 39 skipped, strict actual
counter-to-Usage receipts and explicit preflight reasoning coverage,
plus an **original source** → single draft → legitimate interruption →
same-Run recovery and Oracle pass. Model-native count can already be
accepted for the prior 11/11 matched calls; the end-to-end retrieval
acceptance is a separate gate. GitHub required CI/CodeQL on the actual
new Head and an independent code audit remain separate gates; the
skipped OTel E2E must be disclosed. Until then **PR #98 stays Draft**.
