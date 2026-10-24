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

The script asks for the protocol, complete endpoint, exact provider-native model
ID, documented context capacity, output ceiling, and repetitions. It builds the
current checkout before requesting the API key. Enter the key at the hidden
terminal prompt; do not paste it into a command, report or chat.

| Input | Meaning |
| --- | --- |
| Protocol | OpenAI **Responses** or Anthropic **Messages** |
| Endpoint | Complete API request URL; blank selects the protocol's official default |
| Model | Exact provider-native model ID; do not add the runtime router's vendor prefix |
| Context tokens | The selected model's documented capacity; the script does not infer it from its name |
| Maximum output tokens | Explicit limit on every model request; default 1,024 |
| Repetitions | 1–20 repetitions of each of two cases; default 3, for six task samples |
| Unknown-model schema opt-in | Explicit permission to probe native strict JSON Schema for an unlisted model; default no |
| API key | Hidden input, passed to the already built runner through stdin |

The context capacity must leave at least 6,144 tokens after reserving the output
ceiling. The service input ceiling stays 6,144 tokens so that the task exercises
Context compaction even when the model supports a much larger window. This uses
the existing conservative Context estimate; it does not claim an exact provider
tokenizer or reliable token-admission capability.

Official endpoint defaults are `https://api.openai.com/v1/responses` and
`https://api.anthropic.com/v1/messages`. A gateway may use a different complete
path. The script never appends paths or turns a Chat Completions endpoint into a
Responses endpoint. HTTPS is required except for explicit loopback HTTP used by
local tests. URL credentials, query parameters and fragments are rejected.
Redirects remain disabled by the existing provider transport.

Every run first makes a small native strict-schema probe. An unknown model fails
before dispatch unless the user explicitly enables the exact-model override.
A successful probe is an observation of that endpoint/model configuration; it
does not certify all gateway behavior. Failure never falls back to prompt-only
JSON or a different API protocol. Probe latency and usage are separate from
task sample totals.

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
actual recovered evidence. The shared 6,144-token service ceiling stays fixed. After
the draft's Tool receipt commits, a controlled interruption reconstructs the
runners. The same Run and Turn must complete without executing the committed
Tool call again. The oracle checks source facts, archived evidence, actual
retrieval, the saved draft, output bounds and recovery identity.

This is one task family, not two unrelated capability benchmarks. The draft is
an in-memory fixture; the task does not deploy a release or send a message.

## Reports and comparison

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

Reports do not contain raw model requests, provider replay, HTTP bodies,
authorization headers or raw error text. Error categories are stable and
bounded. The launcher also redacts the entered key from persisted strings and
visible diagnostics. If terminal echo cannot be disabled, key entry fails
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
