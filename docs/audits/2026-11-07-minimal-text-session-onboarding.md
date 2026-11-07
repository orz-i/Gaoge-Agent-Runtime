# 2026-11-07 — Minimal text Session onboarding governance

## Scope and invariants

This is an additive pre-stable developer-experience change on `main@87bad997`.
It does not create a new Agent Loop, Host intent router, Session database,
Kernel state/status, Provider retry policy, ModelInvocation schema, or fallback
protocol. The original Agent / Harness / Kernel contracts remain authoritative.

**Trusted boundaries and required invariants:**

1. The Host authenticates `Actor` and tenant-scoped `HostThread` and owns the
   conversation transcript. `TranscriptHistoryFunc` receives an ordered,
   immutable snapshot of **prior completed pure-text User/Assistant** messages
   and appends the current User message deterministically (`<turn.ID>/user`).
   Messages (including role, content and non-text metadata) are validated
   before any immutable Turn admission; the runtime does not persist an
   additional Host transcript or write assistant messages there.
2. Each Host Turn freezes its complete submitted Context through the existing
   `CreateTurn`/root Invocation/Context checkpoint admission. Repeating the
   same Host Turn before history changes is idempotent. Repeating after the
   original history changes fails closed rather than generating a new model
   request, including duplicate current `SourceID` and changed prior ancestry.
   Previously admitted Turns use `Load`, `Refresh`, or `Resume` to recover.
3. `OpenTextAgentSession` composes exactly the existing `agent.NewRunner` and
   `harness.NewRunner`, and installs the existing Context-run rehydration and
   pre-commit `MaterializeContextWindowRequest`. Optional `RunExecution`
   preserves Kernel Store lease/epoch rules. Provider binding, effect intent,
   receipts, retry/no-effect proof and cancellation are unchanged.
4. The convenience entry is intentionally restricted to basic text Sessions:
   uncomposed Tools, Tool policies, Commands, Roles, Skills, memory policies,
   managed Context policies, shared budgets and delegation fail before
   Session creation. Advanced Hosts retain explicit composition.
5. Memory/SQL Harness Store remains a trusted execution-integrity component
   under ADR 0017. An actor mismatch must fail before reading private Host
   history, invoking a model or resuming a physical Run.

## Implementation and regression surface

- `go/agent-runtime-harness/text_agent_session.go`: optional text-only
  composition; no new persistent fields or execution components.
- `go/agent-runtime-harness/transcript_history.go`: stateless Host history
  adapter, cloned inputs and canonical/deduplicated Source IDs, with
  pre-admission text-only message protocol validation.
- `go/agent-runtime-harness/examples/session`: reference example now uses the
  convenience composition without changing its two-turn oracle.
- `text_agent_session_test.go` and `transcript_history_test.go`: verify duplicate
  submission, new Runner composition over the same Stores, multi-turn Context,
  terminal Load/Resume without new Model effects, changed ancestry/collision,
  cross-Actor reads and pre-transcript Prompt denial, unsupported capability
  rejection, invalid Host history and defensive cloning. An independent
  follow-up found two P2 defects in the first implementation: invalid Context
  could leave an accepted Turn that could not be repaired under the same ID,
  and the text-only adapter could forward System/media/Tool/ProviderReplay
  history. The remediation now rejects both before Turn persistence.
  `text_session_preflight_test.go` verifies a 15-case invalid-message matrix,
  no persisted Turn/model effect for rejected submissions, same-ID corrected
  retry, preserved multipart text and a 65-part content rejection.

The minimal example remains a **Memory Store** demonstration. Its test is not
proof of process-loss persistence, external Provider replay, real billing,
concurrent SQL writes or physical exactly-once Tools. For production, the Host
must supply persistent trusted Stores, exact provider bindings and optional
fenced `RunExecution`; advanced Context compaction and Tool effects require
manual composition and their own acceptance suites.

## Exit evidence

- Targeted regression suite, minimal Session example (`go run` / `go test`),
  full `make go-test`, `go-vet`, `fmt-check`, `tidy-check`, `go-lint` pass.
- Full Go Race suite passed through the PostgreSQL module; the remote command
  was aborted by the session host while entering SQLite (not a test failure).
  SQLite, Redis and HTTP modules were then individually run with `go test
  -race ./...` and passed. The affected Harness regressions also passed a
  fresh targeted Race run after the scope refinements.
- `metadata`, `agent-baseline-test`, TypeScript lint/typecheck/test/build,
  evaluation smoke, benchmark smoke, OTel smoke, A2A product check and clean
  consumer release check all passed in separate local commands.
- Not run: real external Provider sampling, Docker-backed integration,
  coverage/security release gates, independent review of an immutable PR
  Head or GitHub CI. A read-only `codex review --base main` was attempted
  locally but returned HTTP 401 (missing model API authentication); it
  provided **no** independent review findings or sign-off. This local slice
  is **not** a merge authorization.
- Original user-owned untracked Provider probes remain unmodified.

## Follow-up P2 repair verification (2026-11-07)

The subsequent code audit reproduced both defects on the staged onboarding
implementation: a protocol-invalid prior System message could fail **after**
`CreateTurn`, making the corrected same-ID submission conflict; independently,
valid System history and User image/file/audio references could reach a model
through `TranscriptHistoryFunc` despite its advertised text-only behavior.
The previous behavior was reproduced before the permanent tests were written;
the first run of the added regression suite failed (RED) on all invalid cases.

Remediation is restricted to `transcript_history.go`. Each prior Entry now
passes canonical message validation (`model.ValidateMessage`) and a stricter
text-only User/Assistant contract before `Seed` returns to `SessionClient.Prompt`.
There are no new writes, identities, status machines, or duplicate Layer
execution paths. The exact same original `AdmissionHash` and replay rules apply
to an already accepted Turn. `TranscriptSourceFunc` remains the explicit Host
integration point for richer history and different presentation needs.

**GREEN evidence after remediation:** 15 invalid role/content/media/replay
cases rejected with `ErrInvalidRequest` before any Turn or model effect;
corrected source history with the **same** Turn ID completes; multipart text
remains ordered; 65 text content parts are rejected. The full repository Go
unit suite, Go Vet, Go Lint and formatting checks passed. Full affected
Harness package + Session example Race (`go test -race ./go/agent-runtime-harness/...`)
passed, along with Beta metadata and tidy checks. Existing untracked user
probe scripts were not read, edited or staged.

Not run for this repair: external paid Provider sampling, Docker-backed
integration, coverage/security release gates, GitHub CI or an independent
review of the final committed PR Head. This branch remains **not merge-ready**
until normal PR gates are met.

## PR #92 final-Head UTF-8 audit remediation

An independent review of PR #92 at initial Head
`3b8c217501d25525fa36ac98a23f412015e30fc8` discovered a
new P2 variant of the pre-admission invariant: Go text strings containing
invalid UTF-8 bytes passed the model message contract. In the current Prompt,
they could reach the physical Model client, then fail with a Context/hash
conflict after the immutable Turn had been accepted; corrected same-ID retries
could not recover. Malformed prior messages could also be admitted as history.

The correction uses **one model-visible text authority**:
`model.ValidateMessage` now rejects invalid UTF-8 in each text part.
`SessionClient.Prompt` validates the current User message *before* the Host
history lookup and preflights each submitted Context entry's message and
identity before durable Turn creation, including a custom `TranscriptSource`.
Custom sources still own transcript ordering, Tool call relationships and
lineage semantics. The standard
`TranscriptHistoryFunc` performs the same canonical text check for direct
`Seed` callers and rejects malformed/noncanonical source, message and
prior Turn IDs. It does not normalize invalid input into a different identity.

The RED regression matrix reproduced rejection failures for malformed UTF-8
in current User prompts, prior User/Assistant and multipart text, historical
IDs, and custom transcript sources. The GREEN suite asserts no Session/Turn
creation, no Model dispatch, and successful corrected **same-ID** retries.
`model/content_test.go` covers multiple malformed encodings across
User/Assistant/System/Tool roles and valid Unicode; its new fuzz target asserts
that any accepted text message survives a byte-exact JSON round trip.
A local 5-second Go fuzz run exercised over 55,000 inputs without a violation.

No new Agent Loop, permission model, Provider replay behavior, schema
migration, or compatibility path is involved. Real Provider sampling remains
outside this synthetic test. The original paid-Provider probe scripts remain
user-owned and unmodified. Merge admission still requires the **new actual
PR Head** to pass its required CI and final review; prior-Head CI evidence
cannot substitute for new-Head gates.

## PR #92 same-class follow-up — unified pre-seal Unicode identity contract

The subsequent actual-Head review of `ed84512942123f3e613f38607e7d2578a1287a79`
confirmed a new P2 in the **same family**: the text-only checks protected
message content but not Config instructions/model/environment identity,
Host references, ModelOptions JSON strings or ContextSeed instructions.
Different invalid UTF-8 byte values (0x80–0xFF) were accepted and collapsed
to the same JSON-based Config ContentHash. Distinct raw instructions on the
same Turn ID incorrectly replayed the prior success rather than returning
`ErrConflict`. MediaSource.FileID and ProviderReplay.Provider showed a
corresponding metadata serialization alias. These are Unicode
canonicalization aliases, not cryptographic hash collisions.

To avoid repeated field-specific repairs, the original failing Head was
covered by a RED→GREEN matrix and one shared authoritative validator:
`jsoncontract.ValidateCanonicalUTF8` recursively inspects exported
JSON-bound structs, maps (including keys), collections and interfaces,
treating arbitrary `[]byte` as opaque binary and `json.RawMessage` as
strict JSON Unicode text. It rejects invalid bytes and unpaired UTF-16
surrogate escapes before encoding/json can replace them with U+FFFD.
`ValidateUniqueJSONKeys` also rejects these noncanonical JSON payloads.
The same guard is invoked at **Config seal**, **Harness root admission**,
**Context normalization/Open**, **Session identity/history preflight**
and the **model.ValidateMessage** boundary. Existing context lineage,
Kernel receipts, physical Run ownership and Provider protocol are unchanged.

Regression coverage explicitly includes:
- Every invalid single byte (128 variants) across 7 typed Config fields
  and 6 nested JSON-value positions; valid Unicode and the intentional
  U+FFFD rune remain usable.
- Config/Context instructions, model ID, dynamic message identities,
  metadata, raw ModelOptions JSON and invalid Unicode surrogate escapes.
- Direct Harness StartAgentTurn, pure Context.Manager.Open, standard and
  custom TranscriptSource, no Session/Turn persistence, zero Model effects,
  and corrected **same-ID** retries; changed valid configuration still
  rejects via frozen admission hash.
- Model media and ProviderReplay metadata, plus a dedicated fuzz target
  for raw JSON string encoding/escaping alongside the original model-text
  JSON fuzz target.

This is a **pre-stable validation hard cut**; no compatibility bridge, DB
schema change or new Agent Loop was added. Limitations: this validator
covers the declared typed fields at these boundaries; arbitrary
third-party custom MarshalJSON implementations must independently maintain
their JSON identity contracts. Opaque Provider behavior, external billing,
real Provider replay and post-delivery recovery still require their
respective Provider/integration acceptance checks.

Final merge authorization must refer to the precise **new immutable PR
Head**, its required GitHub CI, and a full-chain audit. Previous green CI
cannot authorize a subsequent modified commit.
