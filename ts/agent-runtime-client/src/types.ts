export type RuntimeKind = "agent" | "plan_execute" | "workflow" | "team";
export type RunStatus = "running" | "waiting_input" | "completed" | "failed" | "cancelled";
export type HarnessTurnStatus = "accepted" | RunStatus;
export type HarnessItemKind =
  | "user_message"
  | "agent_run"
  | "agent_message"
  | "tool"
  | "approval"
  | "delegation"
  | "capability_invocation"
  | "interaction"
  | "artifact"
  | "context"
  | "diagnostic";
export type HarnessItemStatus = "started" | "waiting" | "completed" | "failed" | "cancelled";

export type ActorRefDTO = { tenantID: string; actorID: string };
export type ThreadRefDTO = { kind: string; id: string };
export type HostRefDTO = { kind: string; id: string };

export type HarnessItemDTO = {
  id: string;
  turnID: string;
  seq: number;
  kind: HarnessItemKind;
  status: HarnessItemStatus;
  hostRef?: HostRefDTO;
  invocationID?: string;
  parentItemID?: string;
  payload?: unknown;
  createdAt: string;
  updatedAt: string;
};

export type HarnessCommandDTO = {
  id: string;
  trigger: string;
  title: string;
  description?: string;
  capabilityKey: string;
  definitionVersion: string;
  executionClass: HarnessExecutionClass;
  source: "first_party" | "application";
  inputSchema: unknown;
};

export type HarnessInteractionKind = "choice" | "confirmation" | "input";
export type HarnessInteractionStatus = "waiting" | "resolved";

export type HarnessInteractionDTO = {
  id: string;
  turnID: string;
  invocationID: string;
  parentItemID?: string;
  key: string;
  kind: HarnessInteractionKind;
  schema: unknown;
  presentation?: unknown;
  status: HarnessInteractionStatus;
  response?: unknown;
  revision: number;
  createdAt: string;
  updatedAt: string;
};

export type HarnessExecutionClass = "agent" | "team" | "plan_execute" | "workflow" | "application";
export type HarnessInvocationStatus = "accepted" | RunStatus;

export type HarnessCapabilityInvocationDTO = {
  id: string;
  turnID: string;
  parentItemID?: string;
  capabilityKey: string;
  definitionVersion?: string;
  executionClass: HarnessExecutionClass;
  inputHash?: string;
  status: HarnessInvocationStatus;
  attempt: number;
  outputRefs: HostRefDTO[];
  errorCode?: string;
  errorDetail?: string;
  revision: number;
  createdAt: string;
  updatedAt: string;
};

export type HarnessTurnDTO = {
  id: string;
  hostTurn: HostRefDTO;
  status: HarnessTurnStatus;
  revision: number;
  errorCode?: string;
  errorDetail?: string;
  createdAt: string;
  updatedAt: string;
};

export type HarnessTurnSnapshotDTO = {
  turn: HarnessTurnDTO;
  invocations: HarnessCapabilityInvocationDTO[];
  interactions: HarnessInteractionDTO[];
  items: HarnessItemDTO[];
  output?: ResultDTO | null;
};

export type HarnessTurnFeedEventDTO = {
  seq: number;
  turnID: string;
  type:
    | "turn.started"
    | "turn.waiting_input"
    | "turn.completed"
    | "turn.failed"
    | "turn.cancelled"
    | "item.started"
    | "item.delta"
    | "item.completed";
  itemID?: string;
  itemKind?: HarnessItemKind;
  delta?: string;
  message?: string;
  data?: unknown;
  status?: HarnessTurnStatus | HarnessItemStatus | string;
  terminal?: boolean;
  createdAt: string;
};

export type RunDTO = {
  id: string;
  kind: RuntimeKind;
  actor: ActorRefDTO;
  thread: ThreadRefDTO;
  requestID?: string;
  goal: string;
  status: RunStatus;
  revision: number;
  errorCode?: string;
  errorDetail?: string;
  deadlineAt?: string | null;
  endedAt?: string | null;
  createdAt: string;
  updatedAt: string;
};

export type EventDTO = {
  seq: number;
  type: string;
  message?: string;
  data?: unknown;
  createdAt: string;
};

export type RunFeedEventDTO = {
  seq: number;
  runID: string;
  type: string;
  delta?: string;
  message?: string;
  data?: unknown;
  revision?: number;
  status?: RunStatus | string;
  terminal?: boolean;
  createdAt: string;
};

export type CheckpointDTO = {
  id: string;
  kind: string;
  status: string;
  payload: unknown;
  response?: unknown;
  createdAt: string;
  resolvedAt?: string | null;
};

export type ResultDTO = { contentType: string; content: unknown };

export type RunSnapshotDTO = {
  run: RunDTO;
  state: unknown;
  checkpoint?: CheckpointDTO | null;
  result?: ResultDTO | null;
  events: EventDTO[];
};

export type StartAgentRunRequest = {
  thread: ThreadRefDTO;
  input: { content: string };
  clientRunID?: string;
  model?: string;
  toolKeys?: string[];
};

export type StartPlanRunRequest = StartAgentRunRequest & {
  approvalPolicy?: "auto" | "required";
  maxSteps?: number;
};

export type ResolvePlanApprovalRequest = {
  expectedRevision: number;
  decision: "approve" | "reject";
  comment?: string;
};

export type WorkflowNode =
  | { id: string; type: "effect"; effect: { kind: string; input: unknown; fromInput?: never } }
  | { id: string; type: "effect"; effect: { kind: string; fromInput: true; input?: never } }
  | { id: string; type: "wait"; wait: { kind: string; payload: unknown } }
  | { id: string; type: "return"; return: { value: unknown; fromNode?: never } }
  | { id: string; type: "return"; return: { fromNode: string; value?: never } };

export type WorkflowDefinitionDraft = {
  id: string;
  revision: number;
  name: string;
  inputSchema?: unknown;
  outputSchema?: unknown;
  nodes: WorkflowNode[];
  limits?: {
    maxNodeActivations?: number;
    maxEffects?: number;
    maxSegments?: number;
    maxActivationsPerSegment?: number;
    maxStateBytes?: number;
  };
};

export type StartWorkflowRunRequest = {
  thread: ThreadRefDTO;
  input: unknown;
  clientRunID?: string;
  goal: string;
  definition: WorkflowDefinitionDraft;
};

export type ResolveWorkflowWaitRequest = { expectedRevision: number; response: unknown };

export type StartTeamRunRequest = {
  thread: ThreadRefDTO;
  goal: string;
  clientRunID?: string;
  mode: "sequential" | "parallel";
  members: Array<{ id: string; goal: string; toolKeys?: string[] }>;
  join: { mode: "all" | "any" | "quorum"; quorum?: number; failurePolicy?: "collect" | "fail_fast" };
};

export type CancelRunRequest = { expectedRevision: number; reason?: string };
export type CancelRunResponse = { run: RunDTO };

export type WorkbenchSectionDTO = { name: string; available: boolean; content?: unknown; hash?: string };
export type WorkbenchTimelineItemDTO = {
  id: string;
  source: string;
  kind: string;
  status?: string;
  title?: string;
  summary?: string;
  seq?: number;
  createdAt: string;
  data?: unknown;
};
export type WorkbenchDiagnosticDTO = { provider: string; operation: string; code: string; message?: string };
export type TopologyNodeDTO = {
  id: string;
  kind: string;
  label: string;
  status: string;
  runID?: string;
  groupID?: string;
  data?: unknown;
};
export type TopologyEdgeDTO = {
  id: string;
  source: string;
  target: string;
  kind: "sequence" | "dependency" | "delegation" | "handoff" | "produces" | "consumes";
  status?: string;
  data?: unknown;
};
export type TopologyV1DTO = {
  schemaVersion: 1;
  rootNodeID: string;
  revision: number;
  hash: string;
  nodes: TopologyNodeDTO[];
  edges: TopologyEdgeDTO[];
};
export type WorkbenchDTO = {
  overview: {
    runID: string;
    kind: RuntimeKind;
    goal: string;
    status: RunStatus;
    revision: number;
    errorCode?: string;
    errorDetail?: string;
    eventCount: number;
    hasCheckpoint: boolean;
    hasResult: boolean;
  };
  run: RunDTO;
  checkpoint?: CheckpointDTO | null;
  result?: ResultDTO | null;
  sections: WorkbenchSectionDTO[];
  timeline: WorkbenchTimelineItemDTO[];
  topology?: TopologyV1DTO;
  diagnostics?: WorkbenchDiagnosticDTO[];
  hash: string;
};
