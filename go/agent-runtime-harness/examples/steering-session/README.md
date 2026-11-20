# Runnable tool-session and mid-task correction

From the repository root:

```sh
go run ./go/agent-runtime-harness/examples/steering-session
go run ./go/agent-runtime-harness/examples/steering-session -interactive
```

The first command runs a repeatable demonstration. The second waits while the
first lookup is running; type the displayed correction and press Enter.
Both use a fixture model, synthetic schedules and Memory Stores. They make no
Provider calls and do not survive process exit.

Expected results:

```text
queued=1 applied=0 (queue acceptance is not delivery)
applied=1 answer=beta: 18:00
late_input=new_turn answer=alpha: 09:00
fixture_only=true persistence=memory
```

The Host uses `OpenAgentSession` with `Inputs`, starts a real Tool, submits input through
the authenticated Run/Actor/Thread boundary, and reports queue acceptance
separately from application. After completion it refuses a late enqueue and
explicitly creates a new Turn. It preserves the initial prompt, applied user
correction and answer in its own transcript. The test also checks that this
history does not deliver the correction twice on the next Turn.

No fake HTTP authentication or hidden durable inbox is provided. Replace the
fixture model, Host authentication/history and input log explicitly for a
product integration. This demonstrates the complete local Host interaction
path, not real-model reasoning quality, production persistence or native Tool
parallelism.
