# PR #58 — OpenAI Responses reasoning + Tool live-proxy follow-up

Date: 2026-10-21. Branch: `feat/model-provider-adapters`.

## Evidence from user-run real proxy checks (before this fix)

A short-lived HTTPS OpenAI-compatible proxy was used to run
`/tmp/gaoge-provider-live-validation` locally against the existing
`provider/openai` implementation. The API key was **never committed**.

| Model ID | Unary text | SSE text | Standalone Tool round-trip | Full Agent Tool loop |
| --- | --- | --- | --- | --- |
| `gpt-6.1-sol` | PASS | PASS | FAIL first response (`ErrUnsupported`) | PASS |
| `gpt-6-sol` | PASS | PASS | FAIL follow-up response (`ErrUnsupported`) | PASS |

Both models returned 3/4 passes. The failure causes cannot be proven from these
high-level outputs alone. Inspection did identify a definite incompatibility:
`decodeResponse` unconditionally rejected any response containing both a
`reasoning` item and local `function_call` items. This is inconsistent with
the documented stateless Responses reasoning model workflow.

Do **not** use or retain the key value in a fixture, report, issue, or PR.

## Bounded remediation

1. Keep `store:false`; request `include:["reasoning.encrypted_content"]`
   only when local Tool definitions are present, avoiding changes to already
   working text-only calls.
2. Preserve the original ordered Responses output containing encrypted
   `reasoning`, function calls, and assistant messages in the optional,
   provider-bound `model.ProviderReplay` envelope, already used for Anthropic.
3. Before the next model request, verify the replayed Call ID, Tool name,
   JSON arguments and assistant visible text exactly match the Agent's
   already committed, authorized transcript. Then send original items followed
   by local `function_call_output` results.
4. When a reasoning Tool response lacks usable `encrypted_content`, return
   `ErrReplayUnavailable` and do **not** silently drop reasoning, implicitly
   set `store:true`, or claim successful continuation.
5. Distinguish an incomplete provider response via `ErrIncomplete` without
   including remote user text, credentials or provider body in the error.
6. Keep opaque encrypted items in trusted Agent state only; public View
   projections continue stripping the replay data. Kernel/Store schema and
   Agent permissions remain unchanged.

## Tests and merge gate

The fixture suite covers stateless unary Tool response/replay, ordered
reasoning+function+tool output, stream terminal receipt, actual Kernel/Agent
stream Tool loop with durable evidence and sanitized public projections,
tampering/cross-provider replay rejection, unavailable encryption,
incomplete status, and unchanged text-only requests.

**Local fixture success does not imply this proxy now passes 4/4.** Rebuild
and rerun both live model tests, compare the outputs, and only then decide
proxy interoperability. Credentialed tests of the official OpenAI API remain
separate production acceptance.
