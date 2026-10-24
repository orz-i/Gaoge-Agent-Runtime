# Unreleased note — OpenAI Responses assistant phases

## Problem and corrected behavior

OpenAI Responses can return assistant progress messages with
`phase: "commentary"` before a completed answer with `phase: "final_answer"`.
The adapter previously concatenated all message text. A valid final JSON answer
could therefore fail a strict output contract when a progress sentence was
prepended. The same normalization also discarded phases from some later
stateless requests.

The adapter now shares one phase projection between response decoding and replay
validation. A phased terminal response must have a single final answer; its text
alone becomes `Response.Content`. Commentary-plus-local-function output remains
an intermediate Tool step. Original message phases and opaque fields are retained
in the existing `model.ProviderReplay` envelope and replayed in their original
order. Every local Tool call still must match the committed count, order, ID,
name and arguments before a request can be dispatched.

Unknown phases, mixed phased/unphased messages, multiple final answers,
commentary-only terminal output, and final-answer-plus-local-Tool output are
rejected as `ErrUnsupported`. Refusal and unknown output items cannot be hidden
by selecting a final answer. The adapter does not extract a JSON substring,
repair model output, retry with a weaker schema, or enable provider-side storage.
Responses whose message phases are all absent/null retain their prior behavior.

Both unary and SSE terminal decoding use this contract. SSE text deltas remain
progress events; their concatenation is not a structured final-answer contract.
Use the returned terminal `Response.Content` for completion validation.

## Continuation and upgrade

No public Go symbols, HTTP fields, SQL migrations or schema versions change.
Completed phased text-only turns can now carry the existing optional replay
envelope, containing original message items. Agent completion and the existing
bounded completion-repair path clone that evidence into trusted persisted
assistant messages; ordinary public State views continue to omit it. Replay
identity uses the same leading/trailing whitespace normalization already applied
by the Agent, while replay retains the original message content and phase.
Standalone reasoning history is still outside this adapter's supported profile. Tool-bearing reasoning responses
still require encrypted reasoning evidence; missing encryption fails explicitly
rather than being dropped.

A new Session Turn still obtains history from the host's `TranscriptSource`.
That trusted source must supply retained replay evidence when continuing a
phased conversation. The public `Output` and `ViewState` intentionally cannot
reconstruct private replay; this change does not automatically ingest completed
Runs into the host's next-turn transcript.

Replay also has a Context cost. Without an exact host-supplied token counter,
the conservative provider-neutral estimate includes both normalized transcript
authority and opaque replay evidence. The OpenAI wire request sends the verified
original items once. Private replay cannot be moved into an Agent-readable
archive to relieve pressure; large metadata or reasoning evidence can therefore
reach the configured service input ceiling. The SDK's estimate and admission
policy are unchanged; this does not claim provider-accurate token accounting.

Drain affected in-flight Runs before deploying this prerelease. A persisted
replay envelope that no longer projects to its committed normalized message is
rejected before dispatch. Reconcile or quarantine such work through the existing
host process. Do not rewrite frozen requests, rehash historical evidence,
automatically regenerate a settled model invocation, or repeat committed Tools.
Older normalized messages whose phases were already discarded cannot recover
that missing evidence from text alone. There is no compatibility concatenation
path or automatic migration of historical records.

Apply the normal coordinated prerelease rollout and consumer/recovery gates;
never replace an already published Beta tag.

## Baseline diagnostics and evidence boundary

The real-model baseline retains its original native strict-schema probe,
instructions, task oracle, output/call limits and artifact query page size.
Its example service input ceiling is deliberately revised from 6,144 to 8,192:
the full native phase/replay fixture measured 5,876 canonical request bytes,
whose conservative hard estimate was 6,758 before the final recovery dispatch.
Even removing synthetic metadata did not fit the original budget without
unrealistically short identities. The roughly 120 KB source still requires
compaction and retrieval. This policy change is recorded in the measurement
profile and dataset hash, so old and new scores cannot be compared directly.
Historical reports are read using their recorded profile, not the new ceiling.

A failed strict-schema probe can now include an optional `schemaCheck` with only
booleans, fixed type labels and counts. It distinguishes invalid JSON, root/required-field/value violations,
extra properties and unexpected continuation. It never saves response content,
arbitrary property names or values, provider error text, or credentials.
Old reports remain valid and unchanged; no report schema-version bump is needed.

Offline HTTP fixtures reproduce a legitimate commentary-plus-valid-final
response failing before the fix and passing afterward. They also exercise the
complete evidence/draft/Context/recovery task, original phase replay, invalid
final output, and the existing identity and refusal boundaries. They are protocol
and runtime evidence, not real-model capability results. A historical live
failure without its raw response cannot establish whether this defect was its
specific cause. Revalidate the user's endpoint/model configuration with a new
observation before claiming live compatibility.

References:

- [OpenAI reasoning guide: preserving assistant phases](https://developers.openai.com/api/docs/guides/reasoning)
- [Provider adapter contract](../provider-adapters.md)
- [Real-model baseline guide](../agent-model-baseline.md)
