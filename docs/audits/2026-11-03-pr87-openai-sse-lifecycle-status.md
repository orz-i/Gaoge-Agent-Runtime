# PR #87 independent P1 — OpenAI SSE early lifecycle contradiction

**Date:** 2026-11-03
**Audited PR Head (before correction):** `e765723ea67121f7433adc7cf2bdd0fdff25769d`
**Scope:** OpenAI Responses SSE physical lifecycle proof before local Tool execution.

## Finding and verified reproduction

The OpenAI streaming adapter bound `response.created` and `response.in_progress`
to the same physical response ID as `response.completed`, but discarded the
early frames' own `status`, `error` and `incomplete_details`. A gateway
could therefore claim `status:"failed"`, `"incomplete"` or `"completed"`
in an early observation—or include non-null generation-failure/truncation
evidence—and then provide a completed Tool-bearing response with the same
ID. The original code returned executable `ToolCalls`, trusted cumulative
Usage and a successful terminal SSE receipt despite the physical
contradiction. These early frames are progress observations; they cannot
grant or retract terminal completion authority independently.

A new isolated HTTP/SSE regression test first ran against the unchanged
Head. Six contradictory variants failed as expected by **actually returning**
one authorized local ToolCall and one terminal SSE success with no error.
The fixture uses no real provider or external Tool. The same test is GREEN
after the correction.

## Fix

The streaming parser now retains presence-sensitive early `status`,
`error` and `incomplete_details`. For `response.created`, a present status
may be `queued` or `in_progress`; for `response.in_progress`, it may be
`in_progress`. An omitted status retains the original legacy behavior.
Explicit null/empty/unknown/terminal statuses and non-null error or
truncation fields fail with `providerhttp.ErrProtocol` *before* any
later completed Tool result can be consumed. Absent or JSON-null error
and incomplete-details fields remain compatible with valid Responses SSE.

Failure returns **no executable ToolCalls, completed ResponseID/Usage,
stateless replay or successful terminal event**. No new error retry
classification, public model field, Kernel/Agent/Host behavior, persistence
migration, provider fallback or compatibility bridge is introduced.

## Evidence and release gate

- Direct test: `go/agent-runtime-provider/openai/stream_lifecycle_evidence_test.go`;
  public Agent/Kernel regression:
  `go/agent-runtime-provider/openai_early_lifecycle_agent_test.go`.
- The Agent-level fixture proves **zero local Tool Handler side effects**,
  no second completed ModelInvocation, and no fabricated second Usage; the
  first legitimate 17-input/5-output-token response remains accounted.
- Negative paths include early failed/completed/incomplete/queued-at-progress
  statuses, explicit null status and non-null error/truncation evidence.
- Positive paths include queued/in-progress creation, the full
  `created(queued) → in_progress → completed` lifecycle,
  omitted legacy status and explicit-null auxiliary fields.
- Independent reproduction: RED on `e765723`, GREEN after code change;
  focused `go test -race -count=3` passed.
- Check final PR Head's GitHub branch-protection checks before merging.

The pre-existing untracked `scripts/probe_openai_responses.py` and
`scripts/test_probe_openai_responses.py` are unrelated to this fix
and must not be included in its commit.
