export type RuntimeKind = "text" | "plan_execute" | "workflow" | "team";
export type RunStatus = "running" | "waiting_input" | "completed" | "failed" | "cancelled";

export type ActorRefDTO = { tenantID: string; actorID: string };
export type ThreadRefDTO = { kind: string; id: string };

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

export type StartTextRunRequest = {
  thread: ThreadRefDTO;
  input: { content: string };
  clientRunID?: string;
  model?: string;
};

export type StartPlanRunRequest = StartTextRunRequest & {
  approvalPolicy?: "auto" | "required";
  maxSteps?: number;
};

export type ResolvePlanApprovalRequest = {
  expectedRevision: number;
  decision: "approve" | "reject";
  comment?: string;
};

export type WorkflowNode =
  | { id: string; type: "effect"; effect: { kind: string; input: unknown } }
  | { id: string; type: "wait"; wait: { kind: string; payload: unknown } }
  | { id: string; type: "return"; return: { value: unknown } };

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
  diagnostics?: WorkbenchDiagnosticDTO[];
  hash: string;
};
