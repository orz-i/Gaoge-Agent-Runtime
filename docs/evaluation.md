# Runtime evaluation

Gaoge Agent Runtime evaluates runtime correctness separately from model quality. The evaluation package remains a pure capability: it does not own workers, persistence, providers, or external evaluation services.

## Deterministic scenario contract

`go/agent-runtime/evaluation` extends the existing immutable Dataset/Runner/Report contracts with typed runtime scenarios:

- `ScenarioSuiteDraft` compiles into the existing hashed `Dataset`.
- `ScenarioExpectation` records structural evidence only: Run status/revision, ordered journal event types, logical effect counts, error code, and an optional host-computed state hash.
- `ScenarioExecutor` runs deterministic fixtures and returns the same structural evidence as a `ScenarioObservation`.
- The built-in runtime-contract evaluator produces the `runtime_correctness` score without an LLM judge.
- `Baseline` pins thresholds to an exact Dataset hash and reports deterministic regressions.

The framework intentionally does not persist prompts, completions, tool arguments/results, arbitrary state, or arbitrary attribute maps. Scenario-specific fixtures can compute a state hash when exact state identity matters.

## Smoke corpus

The Kernel corpus lives in `go/agent-runtime/evaluation/testdata/runtime-smoke.json`. It covers:

1. terminal state and journal recovery after reconstructing the Runtime over the same durable store;
2. stale CAS fencing;
3. at-least-once transition redelivery with one logical consumption after acknowledgement.

The feature corpus in `runtime-feature-smoke.json` adds deterministic Agent tool execution, Workflow wait/resume, Team child/relation materialization, directed Group Chat sequencing, explicit visible-handoff chaining, and selector retry with durable invocation identity reuse. The A2A module covers discovery/send, streaming terminal delivery, input-required cancellation, and shadow-resume retry with stable message identity. The MCP module covers modern discovery/tool execution, schema fencing before remote execution, and official-SDK legacy negotiation followed by tool execution. A2A and MCP keep these protocol-local corpora under their own `testdata` directories while reusing the same evaluation Dataset/Runner/Baseline contracts, so protocol dependencies do not leak into Core. All matching baselines pin exact Dataset hashes. Any corpus edit changes the Dataset hash, so the baseline must be reviewed and intentionally updated.

Run the visible report with:

```bash
make eval
```

`make eval-smoke` is part of ordinary `make check` and is deliberately fast, deterministic, and network-free. Future broader recovery matrices can use the same contracts in nightly/release suites without making PR correctness depend on wall-clock timing or external model services.

## Task-oriented Agent acceptance

The [task-agent example](../go/agent-runtime-harness/examples/task-agent/README.md)
adds an executable task oracle on top of the existing Agent/Harness APIs. Its
ordinary Go tests verify task output against Tool evidence, bounded Context
artifact retrieval, the original Turn/Run identity after runner reconstruction,
and no replay of a committed Tool effect. These package tests run in `make check`;
they do not alter the structural scenario dataset or its pinned baseline.

The PostgreSQL integration suite separately verifies persisted Kernel and
Harness/Context recovery across a real child-process interruption. This proves a
specific crash boundary with deterministic model fixtures, not general physical
exactly-once execution or language-model reasoning quality.

The example's explicit live-provider mode reuses the task and oracle for a real
model. It requires host credentials and an explicitly supplied model window;
it is not run by ordinary unit or CI gates. Report provider/model identity and
observed output separately from offline acceptance. No success-rate, latency or
cost baseline is inferred from a scripted-model run.

The [real-model baseline launcher](agent-model-baseline.md) adds a local,
interactive entry point to this same example. It runs two fixed data variants
with explicit repetitions, records a dataset hash and measurement profile, and
preserves failed or incomplete observations. A separate native-schema probe
precedes task sampling; its latency and usage are not counted as task results.
The launcher compares compatible reports without installing an evaluation
service or turning a recorded run into an automatically accepted baseline.
`make agent-baseline-test` verifies the launcher offline and is part of
`make check`; real models remain opt-in.
