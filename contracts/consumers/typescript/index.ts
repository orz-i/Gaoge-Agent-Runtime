import {
  RuntimeClient,
  type CancelRunResponse,
  type HarnessTurnSnapshotDTO,
  type RunEventPageDTO,
  type RunFeedEventDTO,
  type RunSnapshotDTO,
  type RuntimeKind,
  type StartAgentRunRequest,
} from "@orz-i/agent-runtime-client";

// Compiled against the packed declarations with NodeNext and skipLibCheck=false.
const client = new RuntimeClient({ baseURL: "https://runtime.example/api/v1" });
const request: StartAgentRunRequest = {
  thread: { kind: "conversation", id: "thread-1" },
  input: { content: "hello" },
  clientRunID: "client-run-1",
};
const started: Promise<RunSnapshotDTO> = client.agent.start(request);
const snapshot: Promise<RunSnapshotDTO> = client.runs.get("run-1");
const events: Promise<RunEventPageDTO> = client.runs.events("run-1", { afterSeq: 1, limit: 10 });
const cancelled: Promise<CancelRunResponse> = client.runs.cancel("run-1", { expectedRevision: 1 });
const feed: AsyncIterable<RunFeedEventDTO> = client.runs.feed("run-1", { afterSeq: 1 });
const hostKind: RuntimeKind = "host.custom_feature";
const turn: Promise<HarnessTurnSnapshotDTO> = client.harness.turns.get("turn-1");
const subtask: Promise<HarnessTurnSnapshotDTO> = client.harness.turns.cancelSubtask("turn-1", "child-1", "stop");
const approval: Promise<HarnessTurnSnapshotDTO> = client.harness.turns.resolveSubtaskApproval(
  "turn-1", "child-1", "checkpoint-1", "approve", "reviewed",
);
void [started, snapshot, events, cancelled, feed, hostKind, turn, subtask, approval];
