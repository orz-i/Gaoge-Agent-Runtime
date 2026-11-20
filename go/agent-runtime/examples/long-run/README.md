# Measure actual long-Run state growth before optimizing it

```sh
go run ./go/agent-runtime/examples/long-run -steps=100 -payload-bytes=1024
go run ./go/agent-runtime/examples/long-run -steps=500 -payload-bytes=1024
```

This executes the real Agent loop, commits every read result, deliberately loses
one already-committed Tool-receipt ACK halfway through, reconstructs the Runner,
and completes the same Run without executing a read twice. The fixture model
derives its next action from the model-visible transcript, not a process-local
step cursor. No Provider calls or credentials are involved.

Read the JSON fields precisely:

- `aggregateStateBytesSubmitted`/`aggregateStateWrites`: successful aggregate
  states observed at the actual Memory `Create`/fenced `ApplyExecution` ports.
  They include the ACK-lost commit once. These are logical submitted JSON bytes,
  **not disk writes, PostgreSQL WAL, database CPU or all grant/lease/outbox costs**.
- `maxAggregateStateBytes`/`finalAggregateStateBytes`: actual serialized states,
  not a struct-size estimate. Consumed requests must be released; the command
  rejects a result that retains them.
- `sampledProcessHeapAllocBytes`: maximum process heap observed every 64
  aggregate mutations. This is sampling, not a true peak or isolated Run delta.
- `loadAfterFaultMicros`: a fresh Kernel load. `firstModelAfterResumeMicros` is
  time from `Resume` to its first fixture-model callback. `resumeRemainingWorkMicros`
  includes **all remaining task steps**, so it is not restart startup latency.
- `recovery=runner_reconstruction_same_memory_store`: this is not survival of
  process exit. A persistent-backend/process-crash experiment remains separate.

Optional `-replay-bytes` supplies synthetic opaque payloads; these are not real
Provider signatures. The profile has no bounded Context compaction, so it
measures explicit unbounded-history growth, not a claim about every production
composition. Steps are capped at 500; per-response text is capped at 64 KiB and
the combined fixture payload estimate is capped at 16 MiB. Large cases are not
ordinary CI timing gates.

Use a fresh process, identical payload/profile/source and repeated samples for
comparisons. Timing is local instrumentation-heavy observation; none of the
tests asserts a speed threshold. Persistent SQL/WAL and process-loss recovery
must be measured before selecting a storage redesign.
