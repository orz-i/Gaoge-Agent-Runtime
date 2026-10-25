# Real-model Agent baseline

This source-checkout workflow runs the existing
[task-agent](../go/agent-runtime-harness/examples/task-agent/README.md) against a
host-selected model and saves both successful and failed observations. It adds
no SDK factory, provider fallback, evaluator service or persistence authority.

## Run locally

Requirements: the repository's Go toolchain, Git, Python 3.9 or newer, and a
terminal that can hide password input. No Python packages need to be installed.
From the repository root:

```sh
python3 scripts/run-agent-baseline.py
# Equivalent interactive Make target:
make agent-baseline
```

To run again with a saved report's public configuration:

```sh
python3 scripts/run-agent-baseline.py retry artifacts/agent-baselines/previous-run.json
# Diagnose a failing provider with only one repeat per case (two task samples):
python3 scripts/run-agent-baseline.py retry artifacts/agent-baselines/previous-run.json --repetitions 1
```

`retry` validates the report and displays its exact endpoint, model, budgets,
repetitions and explicit unknown-model opt-in. It checks those settings against
the current runner, builds the current checkout, and asks only for a new hidden
API key. It starts a new measured run, including the normal native-schema probe;
it does not resume old samples or overwrite the original report. `--repetitions 1`
changes only the new run's sample count, preserving the old report and showing
the changed count in the new configuration. Saved endpoints
are not expanded or silently changed. To correct configuration, use the ordinary
interactive command instead. A successful probe proceeds to the requested task
samples; a failed probe still makes no task calls.

The script asks for the protocol, base URL or complete endpoint, exact
provider-native model ID, documented context capacity, task output budget, and
repetitions. It builds the
current checkout before requesting the API key. Enter the key at the hidden
terminal prompt; do not paste it into a command, report or chat.

| Input | Meaning |
| --- | --- |
| Protocol | OpenAI **Responses** or Anthropic **Messages** |
| Endpoint | Base URL or complete API request URL; the final resolved URL is shown before key entry; blank selects the official default |
| Model | Exact provider-native model ID; do not add the runtime router's vendor prefix |
| Context tokens | The selected model's documented capacity; the script does not infer it from its name |
| Maximum output tokens | This task's per-request output budget; default 1,024, rather than the model's maximum supported output capacity |
| Repetitions | 1–20 repetitions of each of two cases; default 3, for six task samples |
| Unknown-model schema opt-in | Explicit permission to probe native strict JSON Schema for an unlisted model; default no |
| API key | Hidden input, passed to the already built runner through stdin |

The context capacity must leave at least 8,192 tokens after reserving the output
ceiling. The service input ceiling is fixed at 8,192 tokens for this measurement
profile, so that the task exercises Context compaction even when the model
supports a much larger window. This uses the existing conservative Context
estimate; it does not claim an exact provider
tokenizer or reliable token-admission capability.

Official endpoint defaults are `https://api.openai.com/v1/responses` and
`https://api.anthropic.com/v1/messages`. Interactive input resolves only the
following recognizable base URL forms, before reading a key or sending a request:

| Input form | OpenAI Responses | Anthropic Messages |
| --- | --- | --- |
| `https://gateway.example` or its root `/` | Append `/v1/responses` | Append `/v1/messages` |
| `https://gateway.example/v1` | Append `/responses` | Append `/messages` |
| `https://gateway.example/proxy/v1` | Preserve the prefix and append `/responses` | Preserve the prefix and append `/messages` |
| Complete endpoint or another custom path | Preserve the supplied path | Preserve the supplied path |

The launcher displays any expansion and the final URL before key entry. It does
not discover routes, probe alternatives, convert Chat Completions, or change an
endpoint belonging to the other protocol. HTTPS is required except for explicit
loopback HTTP used by local tests. URL credentials, query parameters and
fragments are rejected. Redirects remain disabled by the provider transport.

This convenience belongs only to interactive input. The SDK and the runner's
stdin contract still receive a complete request URL. Existing reports are read
with their original endpoint unchanged, including earlier runs that posted to
a gateway's root URL.

The native-schema probe now uses JSON-only instructions matching the supplied schema (`native-json-schema-v2`). The former request for a plain sentence conflicted with the API output constraint. The preflight version is part of the measurement profile and dataset fingerprint; historical reports remain unchanged. A pass still cannot distinguish native enforcement from a model choosing to follow the compatible prompt.

Every run first makes a small native strict-schema probe. An unknown model fails
before dispatch unless the user explicitly enables the exact-model override.
A successful probe is an observation of that endpoint/model configuration; it
does not certify all gateway behavior. Failure never falls back to prompt-only
JSON or a different API protocol. Probe latency and usage are separate from
task sample totals.

## If a run reports failed

The console and Markdown summary explain the failing stage, the safe error code,
and how many task samples ran. A preflight failure with `attempted = 0` means
that the task cases never started; all requested cases remain skipped.

Preflight events may include an observed HTTP status and a fixed response kind
(`json`, `html`, `event_stream`, or `other`). Raw HTTP headers are not recorded.
A response that was never received has no invented status. Earlier reports
without these fields remain readable.

### See the actual failed response

A failed preflight now includes `responseDebug` by default. The console displays
the captured HTTP response body and the SDK's extracted answer directly; JSON
and Markdown reports preserve the same redacted text. The normal run and
`retry <report.json>` commands need no additional debug flag. Successful
preflights omit this failure-only field.

The observer retains at most 256 KiB of the bytes consumed by the adapter. After
credential redaction, the displayed body is bounded to 256 KiB and the extracted
answer to 64 KiB. `capturedBytes` counts the retained original byte prefix, not
the total response size. `capture` distinguishes complete, unavailable,
incomplete, read-error and over-limit observations; `bodyTruncated` and
`answerTruncated` explicitly identify shortened output. An incomplete body is
shown as the observed prefix, not hidden or presented as a complete response.

The current API key and recognized credential fields are redacted before the Go
event is emitted; `redacted` records replacement. JSON may be reserialized when
redaction is needed. Console control characters are escaped, the extracted
answer has a visible JSON-string representation, and Markdown uses a safe code
fence. These display transformations never alter the answer used by the original
JSON/schema validator. The observer adds no request, retry or body drain.

Historical reports that did not save a body cannot recover it later. They remain
readable and unchanged; a new run is required to collect missing response text.

Task failures also display and save `lastResponseDebug`: the final HTTP body
actually consumed by the adapter in that sample, the physical request count and
last observed HTTP status/type. The observer does not make extra calls. A
response seen before a later local validation failure is **not** automatically
the cause of that failure; inspect the recorded physical/model call counts.
`provider_replay_unavailable` is distinct from generic `provider_unsupported`:
stateless OpenAI reasoning-plus-function continuation requires the provider's
opaque encrypted reasoning item. Its `encrypted_content` payload is suppressed
in the saved debug body while its presence or absence remains observable.
Never bypass replay validation, authorization or the exactly-once-effect guard
to make the model pass. Reports may still contain other response-generated
content, so review them before sharing.

A `strict_schema_probe_failed` event can additionally include `schemaCheck`.
This records only fixed JSON-type labels, booleans and counts: whether content
and valid JSON exist, whether the original validator accepted the fixed schema,
the presence/type of the required `ok` field, whether a boolean `ok` is true,
the count of extra properties, and unexpected Tool/artifact/continuation facts.
Object-field facts are omitted for non-object output. It does not include output
text, property names, arbitrary values, validation errors or fingerprints.
Earlier reports without `schemaCheck` remain valid. The original strict schema
still decides success; diagnostics do not extract JSON, repair a response or
retry with a weaker contract.

### Diagnose native-schema failures

An OpenAI `strict_schema_probe_failed` event can also include `wireCheck` from
that same preflight request. It observes the actual adapter HTTP boundary:

- A separate `GetBody` copy checks the outbound `text.format` type, explicit
  strict setting and semantic equality with the fixed probe schema.
- A bounded response-body observer records the original message order, phase,
  text/refusal/other part counts, per-message JSON/schema validity and equality
  with the adapter's final content. Part-validity counts are explanatory only;
  a valid JSON document can span multiple text parts.
- The optional response `text.format` echo is classified separately. A missing
  echo is not evidence that native schema is unsupported.

The observer captures at most 256 KiB per body in memory and emits at most 16
message summaries. `wireCheck` itself contains no response text, arbitrary
property names, values, IDs, headers, hashes or raw errors; failed preflights
expose redacted body text separately in `responseDebug`. Observation adds no
request or retry and preserves the original body read, close and error behavior.
Incomplete, oversized or unavailable wire captures remain explicitly marked;
they do not become negative capability findings. The normal validator remains
the authority for passing or failing the probe.

| Complete observation | Supported conclusion |
| --- | --- |
| Outbound format/strict/schema does not match the probe | The contract is already wrong at the local HTTP boundary |
| Correct outbound contract; the unique final or sole legacy message is invalid on the wire and exactly matches the adapter content | The received upstream answer violated the requested contract; text assembly did not introduce this failure |
| The unique original final satisfies the schema, but the adapter's final content differs and fails | Investigate the local response projection |
| Multiple messages without phase, an unsupported layout or an incomplete capture | The evidence is ambiguous; do not select whichever text happens to parse |

Even a confirmed upstream violation does not distinguish a gateway forwarding
problem from a routed model's behavior. The HTTP JSON envelope and the JSON
inside its answer are separate contracts. Failure of this deliberately strong
native-schema probe does not establish the Agent's task-solving quality: no task
sample has run. Changing the prompt to coax JSON or stripping code fences would
not validate the original native contract.

Use the observations to choose the next check:

| Observation | Next check |
| --- | --- |
| Root/base URL in an earlier report | Run the updated launcher and inspect the resolved API endpoint before entering the key |
| HTTP 200 with HTML | Verify that the configured address reaches the API rather than a website or gateway page |
| HTTP 200 with JSON and `strict_schema_probe_failed` | The adapter returned a model response, but its projected final output failed the probe; use `schemaCheck` to distinguish invalid JSON, wrong shape/value, extra properties, or unexpected continuation |
| `schemaCheck.jsonValid = false` | Inspect the provider/adapter's final-answer handling and native strict-schema behavior; a JSON HTTP envelope does not imply that the contained answer is valid JSON |
| `schemaCheck.schemaValid = true` with a failed probe | Inspect the reported Tool/artifact counts and continuation flag; the probe also requires a terminal, tool-free answer |
| HTTP 401 or 403 | Check the key, its permissions, and gateway access policy; the status alone does not establish which one is wrong |
| HTTP 404 | Check the complete endpoint and model identifier |
| HTTP 400 or 422 | Check the API protocol, model, native schema support and task output budget |
| HTTP 3xx | Configure the final API URL directly; the client does not forward credentials through redirects |
| `model.upstream_timeout`, no HTTP status | A typed transport timeout occurred before an HTTP response was observed; check network, proxy and endpoint reachability before another live sample. This code alone does not identify DNS, TCP, TLS or waiting-for-response as the failing phase |
| No HTTP status with a generic failure | Check connectivity, timeout and provider availability; do not infer a server status or a particular network phase from a generic error |

The probe's 60-second Context deadline is separate from the HTTP transport's
connection and TLS deadlines. A lower transport deadline may fire first. A
network timeout without an observed HTTP response provides no new evidence
about native-schema support, credentials, model output quality or the task
oracle. Reducing output tokens or sample repetitions does not repair connection
establishment. Historical failures retain their original classification; the
launcher does not infer a new cause from duration alone.

The OpenAI adapter distinguishes `commentary` from `final_answer` in phased
Responses output and preserves original phases on continuation. Its previous
text concatenation could make a valid final JSON answer fail this probe.
Offline fixtures reproduce and cover that defect, including the task's Context
and recovery path. A historical failed report without the original response
cannot establish whether that defect occurred in that particular live request.
Do not infer gateway behavior or a model's general capability from the failure
code alone. See the [adapter upgrade note](releases/unreleased-openai-response-phases.md).

For this small task, start with the default 1,024 output-token budget rather than
copying the model's maximum supported output capacity. Keep the same task output
budget when comparing model reports. After correcting a configuration issue, run
again to produce a new observation; keep the earlier failure reports intact.

## What is held constant

Each sample gets fresh in-memory Kernel/Harness stores and model instances. The
two fixed cases are data variants of one release-evidence workflow:

| Case | Expected deployment code | Expected deployment window |
| --- | --- | --- |
| `release-lime` | `LIME-42` | Wednesday 03:00–03:30 UTC |
| `release-violet` | `VIOLET-83` | Friday 09:15–09:45 UTC |

The model must fetch the synthetic dossier, recover a fact from a deep Context
artifact, save one local draft, and return the required structured fields.
The task instructions suggest a 128-character artifact query page so that the
returned evidence fits alongside both providers' native transcript overhead.
This is a prompt instruction, not a new SDK reader limit; the oracle checks the
actual recovered evidence. The shared 8,192-token service ceiling stays fixed.
After the draft's Tool receipt commits, a controlled interruption reconstructs
the runners. The same Run and Turn must complete without executing the committed
Tool call again. The oracle checks source facts, archived evidence, actual
retrieval, the saved draft, output bounds and recovery identity.

This is one task family, not two unrelated capability benchmarks. The draft is
an in-memory fixture; the task does not deploy a release or send a message.

## Reports and comparison

The phase-support revision raises the baseline's service input ceiling from
6,144 to 8,192. A full native phase/replay fixture measured a conservative hard
estimate of 6,758 before the final recovery dispatch; the former ceiling could
not accommodate that supported transcript. This is an example policy change,
not a new SDK default or a weaker estimate. The roughly 120 KB dossier still
requires archival and actual retrieval, and all task, native-schema and recovery
checks remain in force. The larger ceiling is included in the saved measurement
profile and its dataset hash. Older reports remain readable with their recorded
budgets, but the comparison command rejects old/new profile score comparisons.

Reports are written under the ignored `artifacts/agent-baselines/` directory as
a unique JSON file and a Markdown summary. The script prints their exact paths.
The launcher checkpoints each completed sample as it receives that event.
Cancellation, a malformed runner event or an early runner exit preserves the
observations already collected and marks the run incomplete or interrupted.

The report includes:

- Source revision and working-tree dirty status, task version, dataset hash and
  measurement profile.
- Protocol, endpoint, model and explicit budgets.
- Every attempted sample, its stage, outcome and bounded failure code.
- Oracle and recovery observations, model/Tool call counts and real elapsed time.
- Provider-reported input/output usage and coverage; missing usage is unknown,
  not zero consumption.
- Requested, attempted, passed, failed and skipped sample counts, plus timing
  statistics with their sample count.

End-to-end duration includes model calls, local Tools, Context preparation and
recovery orchestration. The fault-test Store clock deliberately advances for
lease expiry; performance measurement uses the real monotonic clock instead.
Cache/reasoning counters are usage detail, not extra tokens to add again to
input plus output. No price or monetary cost is inferred.

Read a sample's checks together with its `stage`. A false oracle or recovery
check after an early failure can mean that the check was not reached; it is not
by itself evidence of a wrong answer or duplicate runtime execution. The report
separates a repeated committed call from a new Tool call chosen by the model
after recovery.

Failures are retained. Do not repeatedly run a model until it passes and discard
earlier failures when building the reference dataset. A report is an observation;
creating it does not automatically approve or replace a reference baseline.

Run another model with the same workload and output ceiling, then compare the
two exact JSON files printed by the script:

```sh
python3 scripts/run-agent-baseline.py compare \
  artifacts/agent-baselines/BASELINE.json \
  artifacts/agent-baselines/CANDIDATE.json
```

Comparison is offline and never requests a key. It requires compatible task,
dataset and measurement profiles; it refuses to merge incompatible results into
a numerical performance claim. Provider, model, endpoint and source revision may
differ and are shown explicitly. Different context capacities are called out.
Incomplete runs cannot establish an overall improvement. Small sample counts
remain visible: a few runs can establish this configuration's initial baseline,
not a universal model ranking.

## Credentials and output boundary

The API key is never a command-line argument, environment fallback, temporary
configuration file or report field. The launcher removes inherited OpenAI and
Anthropic key environment variables before starting build/runner subprocesses.
The runner receives one bounded stdin JSON object; public configuration and
credential-bearing input use separate representations.

Reports exclude model request bodies and authorization headers. Failed
preflights now retain bounded, redacted HTTP response content and the extracted
answer, including provider error-message content when received; these are local
debug artifacts, so review their contents before sharing them. Transport error
categories remain stable and bounded rather than copying raw network errors.
The launcher also redacts the entered key from persisted strings and visible
diagnostics. If terminal echo cannot be disabled, key entry fails
instead of falling back to visible input. Python strings are not guaranteed to
be securely erased from memory; this workflow makes no such claim.

## Evidence boundaries and offline checks

```sh
make agent-baseline-test
go test ./go/agent-runtime-harness/examples/task-agent -count=1
make check
```

These checks use synthetic keys and local HTTP fixtures. They do not call real
model services. Protocol fixtures exercise the actual OpenAI/Anthropic adapters,
including streaming, strict schema, endpoint and authentication wiring. Launcher
tests cover credential transport, partial reports and comparison gates.

Keep the following evidence distinct:

| Evidence | What it establishes |
| --- | --- |
| Scripted task and local HTTP tests | Reproducible execution and protocol wiring |
| User-run model baseline | Observed model behavior on this task and configuration |
| PostgreSQL child-process recovery integration | Durable recovery across the tested real process-loss boundary |

The live task still uses shared memory stores and reconstructs runners in one
process, with a controllable Store clock. It does not prove survival of actual
memory-store process loss. It interrupts after a known committed Tool receipt;
unknown external effects still require reconciliation rather than blind replay.
See the [Agent task guide](agent-task-workflow.md) for durable-store integration.
