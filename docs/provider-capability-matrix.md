# Provider capability matrix — beta.24, round 1

This table describes **SDK implementation and its admission boundary**, not
a promise that an arbitrary public/private model ID or reverse proxy supports
a feature. Model IDs, prices, context-window size and product feature
availability are Host-owned external metadata. No runtime model-name prefixes,
bundled vendor allowlists, feature auto-discovery service or silent fallback.

| Capability | OpenAI Responses adapter | Anthropic Messages adapter | Gate / evidence |
| --- | --- | --- | --- |
| Plain text, local functions | Implemented (Unary and SSE) | Implemented (Unary and SSE) | Original Tool ID, arguments, terminal ResponseID/Usage and receipts must validate; unit/loopback suites |
| Native strict JSON Schema | Implemented | Implemented | Exact selected model's Host `StructuredJSONSchema=true`; no prompt-only downgrade |
| Reasoning effort | Projected to `reasoning.effort` | Projected to `output_config.effort` | Exact Host `EffortLevels` and sampling restrictions; omitted effort never guessed |
| Mandatory local Tool choice | `required` | `any` | Exact Host `SupportsForcedToolChoice=true`; ordinary optional Tools unaffected |
| User image URL/FileID | Ordered input-image wire blocks | Ordered image/source blocks | Exact Host `SupportsImageInput=true` **and** valid provider wire projection; rejected before Unary/SSE on omission |
| User PDF URL/FileID | Ordered input-file wire blocks | Ordered document/source blocks | Exact Host `SupportsPDFInput=true`, independent of image. URL/FileID still needs Host authorization |
| Referenced audio, video, inline binary | Unsupported | Unsupported | Explicit pre-HTTP failure, no text substitution |
| Stateless Tool/reasoning continuation | Opaque encrypted replay for local Tool flow | Opaque signed Thinking replay for local Tool flow | Original provider/protocol/Tool identity; no cross-provider replay; local loopback tests |
| Provider input-token counting | Optional `POST /responses/input_tokens` | No hard-accurate native counter | Must be same provider/model/request projection; unavailable falls back to conservative hard-byte input bound |
| Native counting time | New quality-only report observation | Not certified | Optional elapsed milliseconds on Count downcall; **not** physical HTTP count, model latency or Usage |
| Hosted Provider tools and native web search | Unsupported | Unsupported | Adapter rejects; existing normalized HostedTools shape is not permission |
| Provider-side Compaction | Unsupported | Unsupported | No current wire/persistence contract; future explicit Host opt-in and opaque replay invariants required |
| Prompt cache placement/retention controls | Unsupported | Unsupported | Usage cache-read/write when provided is not authority to configure provider caching |

## Binding and trust boundaries

The Runtime's original `model.Client`, Provider binding HMAC and frozen
`ModelInvocation` remain the sole physical model execution authority.
Host capability snapshots are copied once at Provider construction. Newly
supported image/PDF booleans are positive grants for **the selected model
only**. Their absence leaves the pre-change text-only fingerprint unchanged;
grant changes rotate the selected-model fingerprint and fence pending work.
A new grant does not bypass provider-specific preflight, source authorization,
Kernel CAS, lease/epoch, local Tool receipt checks or cost/Usage proof.

## External evidence limits

- Official OpenAI `gpt-5.6-terra`: one historical 37/40 full
  evidence-family run on an earlier profile, plus a later single-case
  `distractor-05` successful pilot; distinct configurations and
  immutable hashes in `benchmarks/agent-quality-anchors-v1.json`.
  No public external image/PDF live compatibility test on the new media
  grants has been performed.
- Anthropic: the adapter's exact-media preflight, signed replay, strict
  Schema, SSE and Tool identity are exercised with deterministic loopback
  transport. New live external credential testing is not claimed.
- Gateways: unknown capability sets and unsupported stateless replay
  remain blocked; upstream feature parity must be verified per route.
- OpenAI documents native counting as accurate for the corresponding
  Responses payload and Compaction as a separate server-side/standalone
  feature. Neither is evidence that an arbitrary gateway supports them:
  [Counting](https://developers.openai.com/api/docs/guides/token-counting),
  [Compaction](https://developers.openai.com/api/docs/guides/compaction).

## Next capability work packages (not implemented here)

Native compaction requires a separate immutable returned-window/opaque-replay
protocol, Context provenance and recovery analysis. Hosted Tools require a
single original physical Call ID and Provider-hosted effect settlement
contract. Cache optimization requires repeated, same-profile measured
cache-hit/latency/quality evidence before introducing protocol-specific
placement controls. None should enlarge the Kernel or add a second Agent loop.
