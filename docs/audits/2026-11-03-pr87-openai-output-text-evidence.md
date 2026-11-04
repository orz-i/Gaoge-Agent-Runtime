# PR #87 independent audit — OpenAI output_text physical evidence

Date: 2026-11-03. Reviewed PR Head: `e9dc9c3e9bdf93af20573cfbf726e55d11b773c8` (base `main`). Scope: the OpenAI Responses output projection, unary/SSE terminal receipt, stored provider replay and local Tool authorization. This is a pre-stable beta.24 candidate correction; no release or merge authorization.

## Confirmed finding — P1, current PR Head RED

`projectOutput` used the Go zero value of `outputItem.Content[].Text` without proving that `output_text.text` was physically present and a string. An otherwise valid message could contain `{"type":"output_text","text":null}` or `{"type":"output_text"}` followed by a valid text part and a completed registered `function_call`. The invalid part was silently flattened to `""`; both unary and SSE returned a successful response with the local ToolCall, and SSE emitted a terminal success receipt. This bypassed the PR's intended physical-message evidence fence even when `RequireToolCall` was true.

A related replay risk existed: `verifiedReplay` uses the same projection. A persisted phase/Tool replay containing that malformed content could be reconciled to a valid visible Agent text and sent on a later paid HTTP request. The transcript's valid Call ID/name/arguments cannot repair malformed provider-authored text evidence.

Reproduction: add `TestCompletedMessagePhysicalPhaseAndTextEvidence` to the original Head and run `go test ./openai -run TestCompletedMessagePhysicalPhaseAndTextEvidence -count=1` in `go/agent-runtime-provider`. The null/missing-text cases failed in both unary and SSE because the adapter returned authorized ToolCalls, and the stream emitted success. Legitimate Tool cases passed.

## Compatibility check (important non-finding)

The existing `TestPhaseAbsenceAndNullPreserveLegacyProjection` and `docs/provider-adapters.md` explicitly preserve legacy `phase: null` semantics. A preliminary hypothesis that null phase itself was an authority defect was **rejected** after consulting the project's actual contract. No phase behavior is changed. Only null/missing *text values inside an `output_text` part* are rejected.

## Correction and regression scope

- `openai/phase.go`: in the shared output projection, verify each `output_text` content part has a present JSON string `text` before projecting any sibling Tool. Apply the same physical-presence rule to `refusal.refusal` (already rejected by the previous empty-refusal check). Unknown/malformed types continue to fail closed.
- Completed but semantically rejected Tool-bearing responses return only original trusted `ResponseID`/cumulative `Usage` via the existing `model.post_generation_failed` path. No user text, ToolCall, signed replay or successful terminal SSE event escapes. Incomplete/invalid Usage does not establish a paid receipt.
- `physical_message_payload_test.go`: optional/mandatory Tool cases, unary/SSE, null/missing content text, positive phased/legacy Tool responses (including `phase: null`).
- `replay_message_text_test.go`: null/missing stored text fails **before** continuation HTTP, while an explicitly empty but valid string remains representable and can replay.
- No new Host responsibility, persistence migration, replay-format version, fallback or compatibility shim.

## Verification

On the corrected local working tree: `go test ./... -count=1`, `go test -race ./openai -run 'TestCompletedMessagePhysicalPhaseAndTextEvidence|TestStoredOpenAIReplayRequiresPhysicalOutputText' -count=10`, `go vet ./...` (all inside `go/agent-runtime-provider`) and `git diff --check` passed. Repository-wide and immutable new-Head GitHub CI should be checked separately. The two pre-existing untracked `scripts/probe_openai_responses.py` and `scripts/test_probe_openai_responses.py` are unrelated and must not be staged or modified.
