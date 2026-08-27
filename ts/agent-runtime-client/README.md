# @orz-i/agent-runtime-client

Framework-neutral TypeScript client for Agent Runtime HTTP v1. It owns the
public Runtime contract for runs, event streams, interactions, outputs,
evidence, queues, Agent Manifests, delegated child runs, fan-in joins,
workbench inspection, and administrator recovery operations.

## Install a Beta

```bash
pnpm add https://github.com/orz-i/Gaoge-Agent-Runtime/releases/download/v0.1.0-beta.4/orz-i-agent-runtime-client-0.1.0-beta.4.tgz
```

Beta packages are GitHub Release archives, not npm registry releases. Registry
publication is reserved for stable versions.

## Create a client

```ts
import { RuntimeClient } from "@orz-i/agent-runtime-client";

const runtime = new RuntimeClient({
  baseURL: "https://gaoge.example.com/api/agentruntime/v1",
  headers: async () => ({
    Authorization: `Bearer ${await getAccessToken()}`,
  }),
});
```

`headers` may be static or asynchronous. Every API accepts an optional
`AbortSignal` through its request options.

## Start and observe a run

```ts
const started = await runtime.runs.create({
  clientRunID: crypto.randomUUID(),
  thread: { kind: "conversation", id: "conversation-42" },
  input: { contentType: "text", content: "Prepare a release risk report." },
  environment: { kind: "environment", id: "default" },
});

let lastSeq = 0;
await runtime.events.stream(started.run.runID, lastSeq, (event) => {
  lastSeq = event.seq;
  renderEvent(event);
});
```

The event stream reconnects transient disconnects and skips already-seen
sequence numbers. Persist the latest `seq` in the caller when a stream must
survive a page reload.

Clean stream boundaries include `run.completed`, `run.failed`,
`run.cancelled`, `run.waiting_input`, `run.waiting_handoff`, and
`run.waiting_timer`, and `run.suspended`.

## Publish and start a Dynamic Workflow

Dynamic Workflow complements Agent Team: code owns explicit control flow,
state, budgets, waits, and recovery, while Agent nodes own only bounded
probabilistic work.

```ts
const limits = {
  maxNodeActivations: 100,
	maxEffects: 20,
	maxSegments: 100,
	maxActivationsPerSegment: 16,
	maxFanOut: 8,
	maxConcurrency: 4,
  maxNestedDepth: 3,
	maxAttemptsPerEffect: 3,
  maxStateBytes: 1_048_576,
};

const draft = {
	id: "release-decision",
	revision: 0,
  name: "Release decision",
  inputSchema: {
    type: "object",
    required: ["changeID"],
    properties: { changeID: { type: "string" } },
  },
  outputSchema: {
    type: "object",
    required: ["approved"],
    properties: { approved: { type: "boolean" } },
  },
	nodes: [
		{id: "review", type: "wait", wait: {kind: "author.approval", source: {kind: "workflow_input"}}},
		{id: "result", type: "return", return: {source: {kind: "wait_response", nodeID: "review"}}},
	],
  limits,
	policy: {costClass: "none", maxCostUnits: 0, sideEffectClass: "none"},
} as const;

const proposal = await runtime.workflows.definitions.compile({
	scope: {kind: "actor"},
	draft,
});

const published = await runtime.workflows.definitions.publish({
	scope: {kind: "actor"},
	draft,
	expectedRevision: proposal.baseRevision,
	idempotencyKey: crypto.randomUUID(),
});

const startedWorkflow = await runtime.workflows.start({
  clientRunID: crypto.randomUUID(),
  thread: { kind: "conversation", id: "conversation-42" },
	goal: "Decide whether to release change-17",
	definitionReference: {
		id: published.revision.definition.id,
		revision: published.revision.definition.revision,
		hash: published.revision.definition.hash,
	},
  input: { changeID: "change-17" },
});

const result = await runtime.runs.get(startedWorkflow.run.id);
```

Workflow Definition revisions are immutable and freeze exact Agent Manifest,
nested Workflow, and Tool definition dependencies. Retry a publication with
the same request identity; use `expectedRevision` when revising an existing
definition.

## Publish immutable Agent Manifests

Administrator APIs append immutable revisions. A revision can only narrow the
environment's model, tool, skill, delegation, and call limits. Zero or omitted
call budgets inherit the environment ceiling.

```ts
const researchAgent = await runtime.admin.agentManifests.create({
  name: "Research specialist",
  description: "Collects bounded evidence and returns a concise summary.",
  instructions: "Use approved sources and identify uncertainty.",
  status: "active",
  executionMode: "direct",
  toolKeys: ["search", "files.read"],
  skillKeys: ["research"],
  maxChildRuns: 2,
  maxDepth: 2,
  maxLLMCalls: 4,
  maxToolCalls: 8,
});

const revised = await runtime.admin.agentManifests.revise(
  researchAgent.manifestID,
  {
    expectedRevision: researchAgent.revision,
    name: researchAgent.name,
    description: researchAgent.description,
    instructions: "Use approved sources, identify uncertainty, and cite output IDs.",
    status: "active",
    executionMode: "direct",
    toolKeys: researchAgent.toolKeys,
    skillKeys: researchAgent.skillKeys,
    maxChildRuns: researchAgent.maxChildRuns,
    maxDepth: researchAgent.maxDepth,
    maxLLMCalls: researchAgent.maxLLMCalls,
    maxToolCalls: researchAgent.maxToolCalls,
    revisionNote: "Require output lineage in the summary.",
  },
);
```

`expectedRevision` is an optimistic concurrency boundary. Reload the latest
revision after a conflict rather than overwriting another administrator's
change.

Non-administrator callers can list active manifests:

```ts
const agents = await runtime.agents.list({ limit: 100, offset: 0 });
```

## Delegate an explicit child run

Delegation freezes the selected Agent Manifest revision into the child run.
Parent outputs and evidence are not inherited implicitly; send only the IDs the
child task requires.

```ts
const clientHandoffID = crypto.randomUUID();

const delegated = await runtime.runs.delegate(parentRunID, {
  clientHandoffID,
  agentManifest: revised.ref,
  goal: "Compare the rollout plans and identify unsupported assumptions.",
  contentType: "markdown",
  outputIDs: ["output-rollout-a", "output-rollout-b"],
});
```

Keep `clientHandoffID` stable when retrying an ambiguous network failure. A new
ID represents a new child task; reusing an ID with a different request causes an
idempotency conflict.

Inspect the complete delegated task tree with one call:

```ts
const tree = await runtime.runs.taskTree(parentRunID);

for (const task of tree.tasks) {
  console.log(task.handoff.agentName, task.run.status);
}
```

## Wait for delegated tasks with fan-in

Create a durable fan-in contract after delegating one or more child runs:

```ts
const join = await runtime.runs.createHandoffJoin(parentRunID, {
  clientJoinID: crypto.randomUUID(),
  handoffIDs: tree.tasks.map((task) => task.handoff.handoffID),
  mode: "quorum",
  quorum: 2,
  failurePolicy: "collect",
  timeoutSeconds: 3_600,
  timeoutPolicy: "cancel_pending",
});
```

Modes:

- `all`: resolve after every selected child is terminal.
- `any`: resolve after the first successful child.
- `quorum`: resolve after the configured number of successful children.

Failure policies:

- `collect`: keep waiting while the completion rule can still be reached.
- `fail_fast`: fail after the first failed or cancelled child.

Timeout policies:

- `cancel_pending`: suspend the parent and cancel unfinished selected children.
- `leave_running`: suspend the parent while unfinished children continue.

A failed or timed-out join suspends the parent run and leaves an explicit resume
checkpoint. It does not leave the parent waiting indefinitely.

## Cancellation and recovery

Cancelling a parent run also makes its pending fan-in joins terminal and
propagates cancellation through unfinished delegated descendants:

```ts
await runtime.runs.cancel(parentRunID);
```

Late child results cannot resume a terminal parent.

When a recoverable durable continuation reaches the dead-letter state, an
administrator may inspect and requeue it with an audit reason:

```ts
const deadLetters = await runtime.admin.continuations.list({
  status: "dead_letter",
  limit: 50,
  offset: 0,
});

for (const job of deadLetters.results.filter((item) => item.recoverable)) {
  await runtime.admin.continuations.requeue(job.jobID, {
    reason: "Provider connectivity has been restored.",
  });
}
```

## Interactions and explicit resume

Resolve an input or approval interaction with a caller-owned idempotency key:

```ts
await runtime.interactions.resolve(runID, interactionID, {
  clientResolveID: crypto.randomUUID(),
  response: { approved: true },
});
```

Resume a suspended run from its latest or selected checkpoint:

```ts
await runtime.runs.resume(runID, {
  checkpointID,
  clientResumeID: crypto.randomUUID(),
});
```

Do not explicitly resume a run that is still `waiting_handoff`; fan-in owns that
checkpoint until it resolves, fails, is cancelled, or times out.

## Error handling

```ts
import { RuntimeAPIError } from "@orz-i/agent-runtime-client";

try {
  await runtime.runs.get(runID);
} catch (error) {
  if (error instanceof RuntimeAPIError) {
    console.error(error.status, error.code, error.requestID, error.message);
  }
}
```

Use `code` for stable program logic and retain `requestID` for operational
diagnostics. Human-readable messages may change between releases.
