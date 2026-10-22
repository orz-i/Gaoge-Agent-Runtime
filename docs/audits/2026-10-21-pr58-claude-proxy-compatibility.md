# PR #58 — Anthropic optional content fields compatibility

Date: 2026-10-21. Branch: `feat/model-provider-adapters`.

## Evidence from user-run Claude proxy tests

Proxy: `https://vip.auto-code.net/v1/messages`. Authentication and credentials are intentionally omitted.

| Model alias | Unary | SSE | Tool round-trip | Agent Loop |
| --- | --- | --- | --- | --- |
| `claude-sonnet-5-5` | PASS | PASS | FAIL at first Tool response | FAIL before Tool execution |
| `claude-opus-5-5` | PASS | PASS | FAIL at first Tool response | FAIL before Tool execution |

The real proxy returned HTTP 200 with `stop_reason:"tool_use"` and
`content_types:[text,tool_use]` for Sonnet; Opus returned
`[thinking,text,tool_use]`, with signed Thinking present. These high-level
results do not expose the actual JSON shape of the content blocks, so the exact
real-proxy failure mechanism is still unverified.

## Confirmed code compatibility defect

The previous `parseContentBlock` only allowed the exact legacy field set
(`type,id,name,input` for `tool_use`, `type,text` for text). Current official
Anthropic Messages response types document optional `caller`,
`toolset_name` and `citations` members. A deterministic mock containing
valid `caller:null`, `caller:{"type":"direct"}`, `toolset_name:null` and
`citations:null` failed with `ErrUnsupported` on the original PR Head.

## Bounded remediation

- Accept missing/null or explicitly `{"type":"direct"}` `caller` metadata
  only for already-declared local Tool names.
- Allow absent, null or empty `toolset_name`. Reject nonempty toolsets:
  their members have separate authorization/execution semantics.
- Allow absent/null/empty `citations`. Reject rich nonempty citations until
  the model contract can represent them without silently dropping sources.
- Preserve supported non-null metadata in the queued assistant replay,
  including when rebuilding ContentBlocks from SSE events. Normalize optional
  null `caller` / `toolset_name` / `citations` fields by omitting them from
  subsequent Messages input, as some providers reject `caller:null` there.
  Signed Thinking contents and order remain unchanged.
- Continue to reject server/indirect `caller`, unknown metadata, unknown
  Tools and malformed arguments instead of broadening execution permissions.

The new fixture suite proves the valid fields failed before this change and
now round-trip in both unary and streaming mode. Negative fixtures cover
indirect server callers, toolset membership, nonempty citations and arbitrary
additional fields. No Kernel status, persistence schema, or Tool authorization
policy changes were made.

## Real provider acceptance

**Live 4/4 is not yet verified** for either model after remediation.
A local diagnostic binary at `/tmp/gaoge-anthropic-live-validation`
now reports safe field-presence/shape only (never signatures, Tool arguments,
model text or credentials). The interactive wrapper
`GAOGE_CLAUDE_TEST_MODE=tool bash /tmp/gaoge-claude-test.sh` tests only
the Tool round trip to minimize further paid requests.

Official references:
- [Anthropic Messages API response types](https://platform.claude.com/docs/en/api/messages/create)
- [Anthropic SDK Go ToolUseBlock conversion](https://github.com/anthropics/anthropic-sdk-go/blob/main/messageutil.go)
