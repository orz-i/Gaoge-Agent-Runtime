// Code generated from contracts/agent-runtime/v1/openapi.yaml; DO NOT EDIT.
export type ActorRefDTO = {
  actorID: string;
  tenantID: string;
};
export type ThreadRefDTO = {
  id: string;
  kind: string;
};
export type StartAgentRunRequest = {
  clientRunID?: string;
  input: {
    content: string;
  };
  maxModelOutputTokens?: number;
  model?: string;
  thread: {
    id: string;
    kind: string;
  };
  toolKeys?: Array<string>;
};
export type CancelRunRequest = {
  expectedRevision: number;
  reason?: string;
};
export type RunDTO = {
  actor: ActorRefDTO;
  budgetScopeID?: string;
  createdAt: string;
  deadlineAt?: string | null;
  endedAt?: string | null;
  errorCode?: string;
  errorDetail?: string;
  goal: string;
  id: string;
  kind: string;
  logicalParentRunID?: string;
  parentRunID?: string;
  requestID?: string;
  revision: number;
  status: "running" | "waiting_input" | "paused_budget" | "completed" | "failed" | "cancelled";
  thread: ThreadRefDTO;
  updatedAt: string;
};
export type EventDTO = {
  createdAt: string;
  data?: unknown;
  message?: string;
  seq: number;
  type: string;
};
export type RunFeedEventDTO = {
  createdAt: string;
  data?: unknown;
  delta?: string;
  message?: string;
  revision?: number;
  runID: string;
  seq: number;
  status?: string;
  terminal?: boolean;
  type: string;
};
export type CheckpointDTO = {
  createdAt: string;
  id: string;
  kind: string;
  payload: null;
  resolvedAt?: string | null;
  status: "pending" | "resolved";
};
export type ResultDTO = {
  content: unknown;
  contentType: string;
};
export type RunSnapshotDTO = {
  checkpoint?: CheckpointDTO;
  eventHead: number;
  pendingTool?: PendingToolDTO;
  result?: ResultDTO;
  run: RunDTO;
  state: unknown;
};
export type PendingToolDTO = {
  callID: string;
  deadlineAt?: string;
  kind: "external_result" | "child_terminal";
  ready: boolean;
  toolKey: string;
};
export type RunEventPageDTO = {
  eventHead: number;
  events: Array<EventDTO>;
};
export type RuntimeKind = RunDTO["kind"];
export type RunStatus = RunDTO["status"];
