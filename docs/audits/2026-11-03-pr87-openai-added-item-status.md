# PR #87 OpenAI added-item status audit

## Scope and original evidence

Audited immutable Head `c087c0aa2338e38f465ec9eb643b1b7ca3b495cf`.
The SSE added-item collector fixed item identity but ignored physical
`status`. An added function item marked `incomplete`, followed by matching
`output_item.done` and `response.completed` items marked `completed`,
returned an executable ToolCall, ResponseID/Usage and terminal SSE success.
A public Agent regression reached its local test Tool handler once
(`effects=1`) before failing on that deliberately empty Tool result.

A second RED regression showed that `added.status=completed` arguments
`{"x":1}` could be replaced by `{}` in done/final output. The same omission
affected completed message text/refusals and encrypted reasoning evidence.

The [official Responses streaming events contract](https://developers.openai.com/api/reference/resources/responses/streaming-events)
identifies added/done item lifecycle events. Partial in-progress reasoning
content is not final evidence. An explicit incomplete status or an already
completed payload must not be silently rewritten as a different completion.

## Correction and compatibility

- Reject explicit incomplete, failed, null, empty, unknown or wrongly typed
  added-item status as a protocol error before executable output or billing.
- Preserve genuinely omitted legacy status and ordinary `in_progress`
  items. Their arguments, text and encrypted content may still evolve.
- Preserve `completed` added items by collecting their full terminal
  evidence in a separate completed-item set. Reuse the existing done/final
  reconciliation for arguments, message content and encrypted reasoning.
  A later matching done event is expected, not a duplicate added event.
- Completed added reasoning may supply final encrypted bytes omitted by
  later frames, under the same existing completed-evidence reconciliation.
- No public API, replay format, Store schema, Host fallback or migration.

## Verification

Before the fix, the direct incomplete-status test, the public Agent test
and both completed-argument-substitution cases failed as described above.
After the fix, all 130 new matrix and Agent cases passed ten consecutive
runs with the race detector:

```sh
go test -race -count=10 ./go/agent-runtime-provider ./go/agent-runtime-provider/openai \
  -run 'TestOpenAIContradictoryAddedItemStatusNeverExecutesLocalTool|TestStream(AddedItemStatusCannotBeRewritten|CompletedAddedItemPreservesTerminalEvidence)'
```

Coverage includes function/message/reasoning items; omitted, in-progress,
completed and malformed statuses; optional/required Tools; with/without done
events; completed argument/text/refusal/encrypted-content replacement; and
matching completed payloads. Public Agent invalid-status tests assert zero
Tool effects, no extra billed invocation and a pending rejected attempt.

The full final-source `make check` completed with exit 0, including Go
workspace unit/race/vet/lint, TypeScript lint/typecheck/tests/build,
evaluation/benchmark smoke, OTel/A2A and clean release consumers. Formatting,
`git diff --check` and documentation relative links also passed. An independent
static review of the patch found no remaining blocker.

Exact pushed-Head CI remains a separate merge gate. This audit does not
authorize merging, deployment or live Provider acceptance.
