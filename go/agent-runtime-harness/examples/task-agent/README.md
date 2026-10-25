# Task Agent: evidence, Context and safe Session recovery

This example completes one synthetic release task through the public
`SessionClient.Prompt` and `SessionClient.Resume` APIs. It looks up a release
dossier, recovers a deep fact from an archived tool result, saves one local
draft, and returns a structured answer. The default mode is deterministic and
requires no network, provider credentials, database or evaluator service.

From the repository root:

```sh
go run ./go/agent-runtime-harness/examples/task-agent
go test ./go/agent-runtime-harness/examples/task-agent -count=1
```

A successful default run reports `deterministic_runtime_acceptance`, four model
calls, one lookup, one artifact read, one draft write, the original Run/Turn IDs,
and the verified answer. The release and deployment information are synthetic.

## What actually runs

1. Open a Session with a host-owned `TranscriptSource` and send one stable host
   Turn ID.
2. `lookup_release` returns a large dossier. Its deployment code and window are
   deep in the middle, beyond the beginning and end excerpts.
3. The managed Context preparer archives that exact tool output before sealing
   the next model request. The model receives its precise artifact reference.
4. `read_context_artifact` queries `RELEASE R-17` and returns a bounded excerpt
   from that archive. The model extracts the facts and calls `save_draft`.
5. The local draft is appended and its Tool receipt is committed through the
   fenced Kernel Store. A deliberate Store fault then interrupts the host.
6. Construct a new model instance, Agent Runner, Harness Runner and Session.
   The reopened Session has `Transcript: nil`.
7. An immediate `Resume` is rejected while the old execution lease is valid,
   with no model or tool calls. The fault-test Store clock then advances beyond
   its expiry, and `Resume` completes the same Run and Turn.
8. The oracle verifies the result. A repeated terminal `Resume` makes no calls.

The draft handler deliberately does **not** deduplicate writes. A repeated
execution would append a second draft and fail acceptance, so idempotent fixture
behavior cannot hide a replay bug. It writes only to an in-memory test ledger;
it does not deploy, publish, send a message or modify repository files.

## Explicit composition

`compose.go` shows the host-owned assembly without adding an SDK factory:

- Kernel Store and Harness Store are separate dependencies.
- The archive reader is registered in the Tool registry and explicitly granted
  in `ConfigSnapshot.ToolKeys`, with its policy snapshot.
- The model-window resolver accepts only the exact configured model. Scripted
  mode declares a 16,384-token synthetic capacity. Live mode requires the host
  to supply the selected model's documented capacity.
- `ContextPolicy.MaxInputTokens = 8192` is a separate service ceiling that
  makes this small task exercise compaction, even with a large model window.
  The default Context estimator is conservative; this is not an exact
  provider tokenizer measurement.
- Managed preparation is the last pre-intent projection. Rehydration middleware
  restores committed Context during execution recovery.
- Native `RunExecution` sets an owner and lease, with bounded model/tool calls.
- A named Model middleware tightens `Request.MaxOutputTokens` to the configured
  ceiling. `ReservedOutputTokens` reserves context space; it does not set that
  provider limit on its own.

The service ceiling must still leave room for the selected model's Tool
schemas, instructions and retrieved evidence. Making it too small can archive
the reader's result again or fail admission; increasing the provider capacity
alone does not raise the separate service ceiling. The current 8,192 ceiling
replaces the earlier 6,144 baseline profile after native phase/replay fixtures
exceeded that conservative input allowance. The dataset hash and measurement
profile record this change; results across those budgets are not comparable.

## Oracle and correctness boundary

The oracle checks source facts against the structured answer and the one saved
draft; the exact original dossier in the persisted artifact; an actual deep
query result with a matching content hash; the compacted model-visible lookup;
the output ceiling; and unchanged Run/Turn identities and tool counts after
recovery. Tests also change the source facts and reject an otherwise valid JSON
answer containing an invented deployment code.

The scripted model has no cursor or access to the source fixture. It derives
each action from the model-visible request, including after reconstruction.
These tests establish deterministic runtime behavior. They do not measure how
well a real model chooses tools or solves an unfamiliar task.

The default example reconstructs runners **within one process**, using shared
memory stores and a controllable Store clock. It does not demonstrate survival
of memory-store process loss. Production recovery uses the storage adapter's
authoritative time and durable data; application code cannot advance that clock.

For storage and crash evidence, use the existing integration gates:

- `integration/continuation-e2e/managed_agent_session_recovery_real_test.go`,
  `TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash`, uses separate
  processes and actual PostgreSQL Kernel/Harness stores.
- `go/agent-runtime-sqlite/recovery_test.go`,
  `TestAgentCrashAndResumeInNewProcess`, covers direct Agent crash points with
  SQLite.

This example interrupts **after a known committed Tool receipt**. It does not
claim exactly-once external effects across a crash before that receipt.
An unresolved native Tool dispatch requires the existing outcome reconciliation
path; blindly replaying the handler would violate that boundary.

## Interactive real-model baseline

From the repository root, run:

```sh
python3 scripts/run-agent-baseline.py
```

This is the repeatable path for collecting an initial real-model baseline.
Enter a Responses or Messages base URL or complete endpoint, exact model ID,
documented context window and task output budget. The launcher expands only
root and `/v1` base URLs, preserves gateway prefixes, and shows the final request
URL before key entry. The script builds this example before
requesting a hidden API key, then runs a native-schema probe and two fixed data
variants with explicit repetitions. Successes, failures and incomplete runs are
saved under `artifacts/agent-baselines/` with source/configuration metadata,
measured durations, usage coverage and separate oracle/recovery observations.
Preflight failures show the captured, credential-redacted HTTP response body and
the SDK's extracted answer directly, and save both in JSON/Markdown reports.
No additional debug flag is required. Partial captures and truncation are marked;
HTTP status, response kind and safe failure categories remain separate facts.
Failed OpenAI schema probes also report bounded request/response structure
observations from that same call. Original validation and request counts are
unchanged. Use
`python3 scripts/run-agent-baseline.py retry <report.json>` to reuse a saved
public configuration and enter only a fresh key for a new measured run. No real service is invoked
by ordinary test gates.

See the [baseline guide](../../../../docs/agent-model-baseline.md) for the input
contract, offline comparison command, credential boundary and scope of the
in-process recovery evidence.

## Optional real model smoke

Live mode runs the same synthetic task and oracle with a real provider. It is
explicitly opt-in and is not run by package tests or `make check`. Configure
the provider key in the host environment, and select a model that supports the
adapter's strict JSON output contract and local Tool calls. Unsupported
capabilities fail explicitly; there is no prompt-only structured-output fallback.

For an OpenAI model selected by the host:

```sh
go run ./go/agent-runtime-harness/examples/task-agent \
  -mode=live -provider=openai \
  -model="$GAOGE_MODEL" \
  -context-tokens="$GAOGE_CONTEXT_TOKENS" \
  -max-output-tokens="$GAOGE_OUTPUT_TOKENS"
```

The provider constructor reads `OPENAI_API_KEY` only in live mode. Use
`-provider=anthropic` with `ANTHROPIC_API_KEY` for the Anthropic adapter.
Supply the actual selected model's documented window and an explicit output
ceiling, leaving at least 8,192 input tokens. No real model or window is guessed.

A successful live run reports `live_model_task_smoke`. It is evidence for one
task/configuration, not a general model-quality score or ranking. Tool misuse,
missing evidence, a wrong answer, a provider error or inability to finish after
recovery fails this smoke. Real repeated quality evaluation can reuse
`agent-runtime/evaluation`'s existing Target/Evaluator and dataset contracts;
this example adds no new evaluation framework.
