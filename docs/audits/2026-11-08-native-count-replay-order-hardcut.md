# 2026-11-08 — Native input-count signed replay ordering, hard cut

## Evidence and severity

The official OpenAI `gpt-5.6-terra` one-case `distractor-05`
pilot on source `d674b86` reported `inputCountObservations` of
**12 attempts, 3 accepted, 9 unavailable** (private immutable report
`20261108T033222Z-4271a907.json`, SHA-256
`68c344a0cafce6dba8f005de4e7445dc520d9ed73b3c688f656c50150e36d768`).
These are not physical count-HTTP statuses, so it would be incorrect to
assert that all nine failures had the same cause.

A **new, deterministic** Provider/Context/Router test identified an
actual local cause capable of producing exactly this symptom:
`model.Request` with a function Tool call whose original argument bytes
are `{"z":1,"a":2}`, plus an opaque signed OpenAI reasoning replay
that refers to exactly that argument string. The native OpenAI Adapter
accepts the original request. However Context's general `canonicalJSON`
decodes through `interface{}` and re-encodes, sorting those JSON keys
inside `json.RawMessage` while leaving the Provider replay's
`arguments` string untouched. The Context counter then rehydrates a
different Tool argument spelling; the existing signed replay identity
check correctly rejects it **before any HTTP count request**.
Previously, separate tests for Adapter replay and Context routing
never combined these two boundaries.

Original **RED** result in this branch: the exact replay composite
returned `TokenCountSource=estimated`, `nativeCountHTTP=0`,
instead of `exact`, with 0 generation calls. This was not an OpenAI
server rejection, and not a reason to relax replay validation.

## Trust boundaries and safety invariants

| ID | Contract | Required behavior |
| --- | --- | --- |
| N1 | Frozen Agent request → Context size assessment | Retain generic normalized size estimation and, with an enabled native counter, floor serialized-size/fallback charges at the original typed request length; do not rewrite persisted transcript or Provider replay |
| N2 | Frozen request → Context TokenCountRequest | Preserve the **exact typed** JSON representation, including original `json.RawMessage` object order inside signed Tool arguments |
| N3 | Counter bridge → Provider Router | Validate exact typed round-trip, reject noncanonical outer fields, duplicate JSON keys, aliases, trailing JSON, foreign Provider/model and unsupported counter before HTTP |
| N4 | Provider's signed reasoning and Tool call → input-token projection | Keep the original `verifiedReplay` exact compact-byte Tool argument match, CallID/name equality, encrypted reasoning and output status checks; no normalization or semantic-equality weakening |
| N5 | Native count receipt → Context admission | Only positive bounded native input tokens are accepted; when unavailable, conservatively charge at least the larger of normalized/typed request sizes; counting is not generation, billing or an execution grant |
| N6 | Report → published benchmark | Keep original paid failures and input-count observations immutable; rotate the OpenAI quality dataset hash because the previously used native count path changes |
| N7 | Caller-supplied/hostile TokenCountRequest | Never turn an alias, duplicate key, unknown field, malformed or cross-provider payload into native count authority |

## Minimal corrective change

- The Context Manager still computes its ordinary `serialized`
  `canonicalJSON(model.Request)` for existing size/budget decisions, and
  uses the shared `model.MarshalRequestForInputCount` helper for the
  optional native counter. Its typed JSON Encoder disables HTML escaping,
  preventing another false mismatch for Tool arguments containing `<>&`.
  This keeps the exact immutable request's nested signed JSON bytes;
  it does not change the actual generation request or tool execution.
  **When the typed serialization is longer**, the counter-enabled
  Context budget/byte fallback uses that larger byte length as its
  minimum. A long numeric literal such as `1.0000...` can otherwise
  collapse to `1` in generic JSON normalization, leaving the
  so-called conservative fallback substantially *smaller* than the
  original signed request. Noncounter/legacy Context policies retain
  their original byte-accounting path.
- `provider.ContextTokenCounter` changes validation from
  `canonicalJSON(interface{})` equality to **the same shared typed
  MarshalRequestForInputCount re-encoding** equality, retaining `DisallowUnknownFields`,
  recursive unique-key validation, trailing-token rejection and exact
  routed Provider/model identity. Signed replay validation stays
  byte-strict in the original OpenAI Adapter.
- The previously implicit public `runtimecontext.TokenCountRequest.Payload`
  shape is a deliberate beta.24 **breaking contract change** from
  generic normalized JSON to exact typed/no-HTML-escape JSON. There is **no bridge**
  that silently admits old canonicalized signed payloads. Such payloads
  fail closed, and custom TokenCounter implementations must use the new
  documented contract. No SQL/Kernel schema or persisted Run format changes.
- The `evidence-40-v1` OpenAI count strategy advances to
  `openai-native-input-count-v2-exact-raw-replay-fallback-hard-byte`,
  rotating the OpenAI quality hash from
  `sha256:7905b6abca186865869bcdc89ad8178cc2cb2ec4b5b1ada64aea4356ec97f992`
  to `sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`.
  The scripted 40-case hash and original two-case hash remain unchanged.

## Regression matrix

- **RED → GREEN:** full ContextManager → routed Provider → same-origin
  native `/responses/input_tokens` using a noncanonical-order Tool
  argument, opaque encrypted reasoning, response OutputContract,
  forced Tool choice and `reasoning.effort=high`. After the correction,
  the native read-only endpoint is actually called and returns
  `CountExact=367`; *zero* generation requests are made.
- **Eleven additional valid signed replay variants** cover sorted and
  unsorted Tool argument keys, extraneous whitespace, multiline JSON,
  nested objects, Unicode, escaped Unicode, exponent notation, arrays,
  HTML-sensitive characters (`<>&`) and Unicode line separators.
  Each must reach the native *read-only* count endpoint with `CountExact`,
  without any generation.
- Adversarial mutations of the frozen request: changed Tool argument
  value, semantically identical values in a different *signed key order*,
  foreign replay Provider, duplicate Tool argument keys.
  These cases stay `CountEstimated` with conservative
  hard-byte upper bound and **zero** additional count or generation HTTP.
- **HTML escaping RED → GREEN:** Go's default `json.Marshal` rewrote a
  valid signed Tool string containing `<>&`, while the opaque replay kept
  the original text. The shared typed serializer with `SetEscapeHTML(false)`
  preserves byte identity without weakening `verifiedReplay`.
- **Second RED → GREEN:** valid but very long decimal notation in a
  modified Tool argument, where native replay counting correctly rejects
  the modification. Generic JSON serialization reduced the 3,990-byte
  original typed request to 989 bytes, giving an old 1,138-token hard
  fallback—an unsafe size underestimate. The corrected counter-enabled
  fallback reports at least **3,990 serialized bytes** and retains a
  hard-token bound no smaller than those bytes, with no HTTP generation.
- Existing Provider negative cases: cross-Protocol/model, bogus/unknown
  canonical fields, aliases, extra/trailing documents and wrong model
  identity still reject before HTTP; invalid/zero/huge native receipts,
  redirect and unsupported gateway still reject/fallback.
- Old persisted task histories, expensive upstream calls, prior
  benchmark results and the user's untracked Provider probing scripts
  are not modified.

## Unresolved verification

This deterministic RED/GREEN proves a *local* counting defect and its
fix. It **does not prove** that all nine unavailable counts on the
2026-11-08 paid run were caused by argument-order normalization, that
the official remote count API will accept all signed histories, or
that `gpt-5.6-terra` will complete `distractor-05` after the fix.
The original task's trusted terminal cause remains unknown because its
report predates optional `preInterruption`. No real Provider
requests were made during this corrective pass.

The changed measurement profile must be tested under a **new** real
one-case pilot with local hidden credentials and explicit operator
budget, then reviewed separately from the earlier failed pilots.
The PR's actual replacement Head CI/CodeQL, all-module tests and
independent review remain release/merge gates.
