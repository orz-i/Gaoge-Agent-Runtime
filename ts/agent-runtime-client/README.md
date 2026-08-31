# @orz-i/agent-runtime-client

Framework-neutral TypeScript client for Agent Runtime HTTP v1. The client exposes
feature-specific execution APIs, shared run inspection and feeds, and Harness
interactions. Host application data and authorization remain owned by the host.

## Install a Beta

```bash
pnpm add https://github.com/orz-i/Gaoge-Agent-Runtime/releases/download/v0.1.0-beta.5/orz-i-agent-runtime-client-0.1.0-beta.5.tgz
```

Beta packages are GitHub Release archives, not npm registry releases. Registry
publication is reserved for stable versions.

## Create a client

```ts
import { RuntimeClient } from "@orz-i/agent-runtime-client";

const runtime = new RuntimeClient({
  baseURL: "https://runtime.example/api/v1",
  headers: { Authorization: "Bearer <access-token>" },
});
```

Replace the URL and token with those of your configured host. `headers` may
also be a synchronous or asynchronous function. Request options accept an
`AbortSignal`; the client also accepts a custom `fetch` implementation.

## Start and observe an Agent run

```ts
const started = await runtime.agent.start({
  clientRunID: crypto.randomUUID(),
  thread: { kind: "conversation", id: "conversation-42" },
  input: { content: "Prepare a release risk report." },
});

let lastSeq = 0;
for await (const event of runtime.runs.feed(started.run.id, { afterSeq: lastSeq })) {
  lastSeq = event.seq;
  console.log(event);
}
```

Run IDs are returned as `snapshot.run.id`. Start runs through their feature
namespace, such as `agent.start`, not `runs.create`. Snapshots expose only the
current aggregate plus `eventHead`; durable Kernel event history is paged with
`runs.events(runID, { afterSeq, limit })`. Semantic live/replay feeds are async
iterables returned by `runs.feed`, not callback-based `events.stream`.

The feed reconnects transient disconnects, skips already-seen sequence numbers,
and ends after an event marked `terminal`. Persist `lastSeq` if the caller needs
to reconnect after a reload. The optional `onCursorExpired` callback receives
a replacement snapshot when the server reports an expired cursor.

## Supported client surface

| Namespace | Methods |
| --- | --- |
| `agent` | `start` |
| `plans` | `start`, `approve` |
| `teams` | `start` |
| `workflows` | `start`, `resolveWait`, `cancel`, `trace` |
| `workflows.definitions` | `compile`, `publish`, `list`, `get`, `setActivation` |
| `runs` | `get`, `events`, `cancel`, `workbench`, `feed` |
| `harness.commands` | `list` |
| `harness.turns` | `get`, `feed`, `resolveApproval`, `resolveInteraction`, `retryInvocation` |

Use the exported request types for each feature. Having a client method does not
grant permission: the host decides which routes and capabilities are available
to a caller. Generic `admin`, `agents`, `events`, and `interactions` namespaces,
and generic run delegation/resume methods, are not part of this client.

## Harness input and approval

Use the Turn and interaction IDs returned by the host's Harness snapshot:

```ts
const turn = await runtime.harness.turns.get("turn-42");
console.log(turn.interactions);

await runtime.harness.turns.resolveInteraction(
  "turn-42",
  "interaction-7",
  { answer: "Use the approved production brief." },
);
```

The response shape is defined by the specific host interaction. Host policy may
require `resolveApproval` instead; do not substitute a generic run resume for a
Harness decision or a Workflow wait.

## Cancellation

Read the current snapshot and provide its revision when cancelling:

```ts
const latest = await runtime.runs.get(started.run.id);
await runtime.runs.cancel(latest.run.id, {
  expectedRevision: latest.run.revision,
  reason: "Cancelled by the caller.",
});
```

Use `workflows.cancel` for Workflow-specific cancellation and compensation.
A revision conflict requires reloading current state before making a new decision.

## Error handling

```ts
import { RuntimeAPIError } from "@orz-i/agent-runtime-client";

try {
  await runtime.runs.get(started.run.id);
} catch (error) {
  if (error instanceof RuntimeAPIError) {
    console.error(error.status, error.code, error.requestID, error.message);
  } else {
    throw error;
  }
}
```

Use `code` for program logic and retain `requestID` for diagnostics. The release
consumer gate type-checks all TypeScript examples above against the packed
client. Examples require a configured host when executed; the gate does not
make model or provider calls.
