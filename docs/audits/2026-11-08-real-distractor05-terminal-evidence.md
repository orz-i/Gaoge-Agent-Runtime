# 2026-11-08 — Real distractor-05 pilot: post-Tool terminal attribution

## Observed outcome (immutable real-model evidence)

Source report: `artifacts/agent-baselines/20261108T033222Z-4271a907.json`
(SHA-256 `68c344a0cafce6dba8f005de4e7445dc520d9ed73b3c688f656c50150e36d768`).
Report source Head: `d674b86d897274e420f6d5efd4fbfc62488d96c1`,
`dirty=false`, certified OpenAI Responses `gpt-5.6-terra` with
`reasoningEffort=high`, `contextTokens=1050000`,
`maxOutputTokens=128000`, Host Context cap 32768. The original JSON and
Markdown report were read but **not modified or committed**.

The pilot attempted exactly `distractor-05` (39/40 skipped) and returned
`pilot_sample_failed`; this is **not** a 40-case benchmark score.
Strict Schema preflight and stateless reasoning+encrypted Tool continuation
passed; the probe actually observed encrypted reasoning. The task had
8 model invocations, 8 Tool handler invocations (1 release lookup + 7 Artifact
reads, 0 drafts), and completed task Usage receipts covering 10,728 input
and 876 output tokens, 466 output reasoning tokens. Artifact reader calls
comprised 4 query-backed reads and 3 queryless offset pages; 1 candidate
page, 3 model-visible search-hinted pages, 2 terminal pages. Model source
strategy therefore did exercise the new enhanced Tool capability, but did
not save a draft.

Native input counter was invoked 12 times; 3 positive results were accepted
and 9 were unavailable. These counts are **counter interface observations**,
not proven HTTP count requests or billable model operations. The last
accepted input count is **not** proof of the next native count result, and
unavailable results preserve the conservative byte fallback. Actual
native failure cause/status was not preserved in the historical report.

### Important physical receipt fact

The last captured HTTP 200 SSE `response.completed` contained a single
`read_context_artifact` Tool call with `offset=49368`,
`limit=300`, and no `query`. A read-only check computed the exact
SHA-256 key used by the Host's `toolExecutions.callIDHash` for that
Tool key and wire `call_id`. It matched **exactly one of the 8 completed
registered Handler execution receipts** (the eighth).
Therefore **the last observed model Tool call did execute**; the report
does not support claiming a duplicate Tool call ID was rejected or that
the last call failed before dispatch. The resulting task failed before
the expected committed-draft interruption; the original report only
had `task_failed_before_interruption`, without the trusted Harness
Turn status or Agent Run error code. It would be unsound to infer a
Provider fault, exhausted model/tool hard call limit, or a precise
Context failure from these facts.

Both 4,096 and 128,000 output ceilings give an effective input cap of
32,768 for the configured 1,050,000-token model capacity and Host policy.
The output ceiling is not the number of output tokens consumed and does
not explain the failure by itself. This paid pilot used `high` reasoning
effort; earlier pilots used different profiles and cannot be treated as
same-condition numeric improvements.

## Corrective governance

The Host `runTask` now emits an optional `preInterruption` object only
when an initial Prompt fails to produce the expected committed-draft
interruption. It contains `turnStatus` (one of the actual Harness
TurnStatus enum values) and, only for `failed`, an optional fixed
`agentErrorCode` from a strictly enumerated list of Agent-owned
codes. It **never** emits Store `ErrorDetail`, arbitrary
`Turn.ErrorCode`, raw Provider errors, Artifact IDs or request bodies.
The original public `failureCode` and typed cause precedence are
unchanged. Earlier reports without the field remain valid; their
root causes are not retroactively inferred.

Systematic tests include a real Agent/Store path where an in-scope model
reuses a Tool call ID: the Agent correctly fails before executing the
duplicate Tool, and the new Host report independently records the
fixed `agent.tool_invalid` code. This is a **synthetic regression**
of diagnostic fidelity, **not an attribution of the paid pilot**.
Negative tests reject malicious ErrorDetail/unknown codes, completion
statuses falsely claiming failure, missing/extra fields, wrong types
and forged public report data.

## Current gates / unresolved blockers

- No billable API requests were made for this corrective pass. Any new real
  GPT-5.6 Terra run must be explicitly executed from the operator terminal
  with a hidden local API key and the trusted model profile, using the bounded
  `make agent-quality-distractor-pilot` target.
- The genuine paid run's precise terminal cause **remains unknown from its
  immutable report**. The new optional trusted Turn evidence is intended
  to prevent this recurrence; if the Turn has no safe known code, it remains
  explicitly unclassified.
- Native count 3/12 acceptance is a separate compatibility problem, not
  evidence that a Provider generation failed. Physical-count transport
  diagnostics and real latency impact remain unverified.
- PR #98 was a draft stacked on #96. Its prior Head
  `d674b86` passed GitHub CI, CodeQL, coverage, integration and dependency
  checks. The new diagnostic commit must be pushed and rechecked on
  its **actual** PR Head; old CI is never reused as proof of the replacement.
  OTel Collector E2E is skipped in the PR workflow; independent external
  authenticated reviewer unavailable. Merge remains blocked pending
  required gates and a separate actual-model outcome decision.
