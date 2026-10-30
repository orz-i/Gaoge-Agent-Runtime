# P3 — canonical ordered multimodal Model Message hard cut: independent local audit

**Date:** 2026-10-30
**Protected base:** `main@26ba0a86f4159f117ed1951d24b537bd3374d712` (merged PR #79)
**Implementation/docs source Head:** `35154661edc301a6f227a655c87b36dcee2d46aa`
**Local-only governance branch:** `governance/model-message-multimodal-hardcut-20261030`

## Baseline defect and architectural decision

The previous `model.Message.Content string` could represent only
plain text. Core, Provider adapters, Context checkpoint hash,
portable archival, streamed continuation and persistence all assumed
that scalar. Adding a separate media/attachments list would create
competing content authorities and permit media to be silently
discarded when a text-only Provider adapter serialized a request.

P3 therefore deletes the scalar `Content` representation and adopts
one canonical ordered `[]model.ContentPart`, with no legacy string
unmarshaller or compatibility shim. The closed part types are
`text`, `image`, `file` (PDF), and `audio`. Images accept
PNG/JPEG/WebP/GIF, PDFs `application/pdf`, audio
`audio/mpeg` or `audio/wav`. Each non-text part has exactly one
`MediaSource` (`url` or `file_id`) and MIME. URLs must be HTTPS
without embedded credentials, fragments or data-URI bytes. The Host
is responsible for validating the right to use an external URL or
a file reference in a **specific Provider binding/tenant**.

Only `RoleUser` messages may include media. System, Assistant and
Tool visible content remain text-only. Tool calls/Call IDs and
signed/encrypted `ProviderReplay` remain independent authorities;
response `Content` remains textual. Binary payloads are not stored
in Model State, and the higher-level Agent/Harness/HTTP Start Goal
remains text-only (no phantom upload API).

## Provider wire contracts

The standalone OpenAI Responses adapter translates ordered User
parts to `input_text`, `input_image` (`image_url` or `file_id`)
and `input_file` (`file_url` or `file_id`). The Anthropic
Messages adapter uses native `text`, `image` and `document`
parts with `source.type=url` or `file`. Both decline
reference-only audio **before any HTTP call**; they do not
download, decode, flatten or automatically transcribe remote
media. Unknown or role-incompatible content fails closed.
Provider-selected models still determine their own actual media
capabilities; local `httptest` wire correctness does not certify
a live endpoint or third-party gateway.

OpenAI/Anthropic streaming tests prove that ordered image/PDF
input is unchanged when SSE is enabled, while original terminal
message identity, usage and final text remain authoritative.
Existing signed reasoning and Tool replay tests still pass.

## Durable Model intent, Context and token safety

`CloneMessages` and `CloneRequest` deep-copy media sources.
The exact frozen ModelInvocation request/hash covers ordered
media URLs, FileIDs and MIME, with post-intent URL modification,
MIME mutation or part reordering rejected before Provider dispatch.

Context checkpoint lineage and serialized JSON roundtrips preserve
original media without aliases. Portable Context compaction
keeps the full removed multimodal transcript inside hashed
Artifacts, but **never elevates old media URLs/FileIDs into
synthetic System or Context-extract instructions**. Tool-result
compaction still operates only on the proven text-only Tool role.

Media URI byte size is not a physical upper bound on decoded
image/PDF/audio input tokens. Managed Context `AssessModelRequest`
requires a successful trusted, provider-aware exact
`TokenCounter` for any media block, otherwise returns
`ErrModelWindowUnknown`; the original conservative text-only
byte upper bound remains. Unmetered custom Model Clients and
Provider billing remain Host responsibility.

## Independent regressions and original consumers

- `TestMessageOrderedMediaContractAndDurableClone`:
  ordered text/image/text/PDF persistence and pointer cloning.
- `TestMessageMediaFailsClosedOnMalformedPartsAndUnauthorizedRoles`:
  invalid MIME/HTTPS/data URI/mixed sources, unknown kind, role
  mismatch and legacy string-Content JSON all rejected.
- `TestContextMultimodalLineageSealsOrderedSourcesWithoutLoss`:
  durable Context hashes/clone isolation.
- `TestMediaContextHardTokenBudgetRequiresExactProviderCounter`:
  unknown media budget fails, valid exact counter passes,
  text-only fallback unaffected.
- `TestPortableMultimodalArchivePreservesMediaWithoutPromotingUserSources`:
  archived media exact and private refs absent from synthetic Context.
- `TestDispatchModelRequestHasOnlyMonotonicPermissions`:
  post-intent media URL/MIME/order mutation blocked.
- `TestOpenAIResponsesOrderedImageAndPDFWireAndNoAudioFallback` and
  `TestAnthropicMessagesOrderedImageAndPDFSourcesAndNoAudioFallback`:
  actual local HTTP request framing, ordered 4-part bodies and
  unsupported audio never sent.
- `TestOpenAIResponsesMultimodalStreamingKeepsFrozenInput` and
  `TestAnthropicMessagesMultimodalStreamingKeepsFrozenInput`:
  streaming media bodies + final SSE receipt/usage.
- Focused Core and Provider media tests repeated **8x with
  the race detector**, PASS.
- All existing Go module tests across Agent, Context, Provider,
  Harness, PostgreSQL, SQLite, MCP, A2A, HTTP, OTel and
  task-agent, including legacy text-only Go consumers, PASS.
- Format, tidy, vet and lint checks PASS with zero reported
  Go lint issues before the exact final source suite.

## Independent real-integration blocker and repair

The first exact-source release gate sequence reached
`make integration` and failed at the separate
`integration/continuation-e2e` Go module (`GOWORK=off`),
which is deliberately excluded from regular Go workspace
`make go-test`. The fixture still compiled its old Tool
message `Content` as a scalar string in two JSON decoders
and one request diagnostic. All actual PostgreSQL and
Harness PostgreSQL tests had passed; the failure was a
**compile-time regression** in the standalone real E2E
consumer, not an unverified external side effect.

The correction is the fourth atomic commit `3515466`:
decode Tool reply JSON only from checked
`model.PlainText(message.Content)`, fail when a supposedly
text-only Tool contains media, and preserve structured
media evidence in diagnostic transcripts. The standalone
`GOWORK=off` module compile then passed, followed by a
new full actual PostgreSQL/Redis `make integration`:
`TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash`
passed, as did the complete real service/worker matrix.
No production API, replay authority or SQL schema was
changed by this test-only correction.

The exact final-source five-gate matrix below is rerun
**after** that correction; the earlier failed
`make integration` is neither concealed nor counted
as passing evidence.

## Cutover and deliberately unsupported scope

Existing published beta.23 tags remain immutable. The new Content
JSON array is **not** compatible with old persisted scalar string
Content. Before activating a beta.24 candidate, the Host must
stop old workers, inventory all nonterminal Agent Runs,
ModelInvocations and Context/session checkpoints, then drain,
reconcile or quarantine legacy string-Content work. Do not
transcode signed ProviderReplay, rehash old frozen Model intents,
fabricate media/FileIDs or redispatch uncertain physical effects.

This change adds **no** Kernel status, SQL DDL/schema number,
media byte ledger, FeatureDriver, duplicate attachment field,
legacy compatibility bridge, audio transcription, video support,
binary upload endpoint or multimodal Tool/Assistant output.
Synthetic HTTP requests are not live credentialed media tests.
Further Provider capability/Host authorization and exact token
counter implementations remain separate integration work.

## Final exact implementation-Head acceptance

All five release-quality local gates were independently rerun to
completion against the **corrected and committed** Head
`35154661edc301a6f227a655c87b36dcee2d46aa`,
which includes the real integration fixture correction. The
subsequent audit-only commit changes no executable source or
public API definitions.

| Gate | Result | Measured evidence |
| --- | --- | --- |
| `make check` | **PASS, exit 0** | Go tests/Race/Lint/fmt/tidy/vet across all modules, TypeScript test/typecheck/build, evaluation/benchmarks, clean Go/TS release consumers |
| `make coverage` | **PASS, exit 0** | Go statements **73.70%** (70% floor); TS statements **88.08%**, branches **76.47%**, functions **92.85%**, lines **92.13%** |
| `make security` | **PASS, exit 0** | **Zero detected reachable Go-code vulnerabilities**, npm high-severity audit clean. Govulncheck still notes **three required Go dependency module advisories** with no detected reachable vulnerable call paths |
| `make integration` | **PASS, exit 0** | Real PostgreSQL 16 and Redis 8 with Store/worker recovery; `TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash` and `TestRealPostgresManagedSessionCompactionAcrossStoreRestart` both **PASS**; containers/network removed |
| `make integration-otel` | **PASS, exit 0** | Pinned real OpenTelemetry Collector 0.149.0 trace/metric emission E2E **PASS**; container/network removed |
| Focused P3 race tests | **PASS** | Eight repeated race-detector runs of Core/Context/Agent media and frozen request cases, and eight repetitions of Provider media HTTP wire and unsupported-input tests, in addition to the full Go Race matrix |

Local logs (not published):
- `/tmp/gaoge-p3-final-corrected-20261030.log`
- `/tmp/gaoge-p3-final-corrected-20261030-check.log`
- `/tmp/gaoge-p3-final-corrected-20261030-coverage.log`
- `/tmp/gaoge-p3-final-corrected-20261030-security.log`
- `/tmp/gaoge-p3-final-corrected-20261030-integration.log`
- `/tmp/gaoge-p3-final-corrected-20261030-integration-otel.log`
- `/tmp/gaoge-p3-p3-core-race-20261030.log`
- `/tmp/gaoge-p3-p3-provider-race-20261030.log`

**Local decision: GO for pushing the branch, creating a PR and
requesting independent exact-final-Head GitHub merge admission
review.** This is **not** a merge authorization or coordinated
`v0.1.0-beta.24` root+11-module publication approval. No
live credentialed vision processing was attempted; model-specific
remote media capabilities remain Provider/Host integration
requirements rather than claims proved by synthetic HTTP fixtures.

The original user-owned untracked OpenAI probe scripts remained
byte-for-byte identical. No branch was pushed, no PR created,
no release tag published, and no unrelated Worktree altered.
