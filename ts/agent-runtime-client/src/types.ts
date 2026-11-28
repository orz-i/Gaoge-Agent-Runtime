import type { ResultDTO, RunSnapshotDTO, RunStatus } from "./types.gen.js";
export * from "./types.gen.js";
export type HarnessTurnStatus = "accepted" | RunStatus;
export type HarnessItemKind =
  | "user_message"
  | "agent_run"
  | "agent_message"
  | "tool"
  | "approval"
  | "delegation"
  | "agent_invocation"
  | "interaction"
  | "artifact"
  | "context"
  | "budget"
  | "subtask"
  | "diagnostic";
export type HarnessItemStatus = "started" | "waiting" | "completed" | "failed" | "cancelled";

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

export type HarnessInvocationStatus = "accepted" | RunStatus;

export type HarnessAgentInvocationDTO = {
  id: string;
  turnID: string;
  parentItemID?: string;
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

/** Committed Harness status, independent of Host assistant-output acknowledgement. */
export type HarnessTurnStateDTO = { turn: HarnessTurnDTO };
/** Cursor-paged, publicly filtered Harness Timeline facts. */
export type HarnessTimelinePageDTO = { items: HarnessItemDTO[]; nextAfterSeq: number };

export type HarnessTurnSnapshotDTO = {
  turn: HarnessTurnDTO;
  invocations: HarnessAgentInvocationDTO[];
  interactions: HarnessInteractionDTO[];
  items: HarnessItemDTO[];
  output?: ResultDTO | null;
  budget?: HarnessBudgetDTO;
  subtasks?: HarnessSubtaskDTO[];
};

export type RuntimeBudgetLimitsDTO = {
  maxLLMCalls?: number;
  maxToolCalls?: number;
  maxInputTokens?: number;
  maxOutputTokens?: number;
  maxTotalTokens?: number;
  maxChildRuns?: number;
  maxConcurrentRuns?: number;
  maxOutputBytes?: number;
  maxStateBytes?: number;
  maxCostUnits?: number;
};

export type RuntimeBudgetUsageDTO = {
  llmCalls?: number;
  toolCalls?: number;
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  reasoningTokens?: number;
  childRuns?: number;
  outputBytes?: number;
  stateBytes?: number;
  costUnits?: number;
};

export type HarnessBudgetDTO = {
  scopeID: string;
  revision: number;
  limits: RuntimeBudgetLimitsDTO;
  usage: RuntimeBudgetUsageDTO;
  reserved: RuntimeBudgetUsageDTO;
  remaining: RuntimeBudgetUsageDTO;
  activeRuns: number;
  waitingRuns: number;
  unknownUsage?: boolean;
  cancelled?: boolean;
};

export type HarnessSubtaskDTO = {
  id: string;
  runID?: string;
  parentID?: string;
  kind: string;
  memberID?: string;
  memberRevision?: string;
  goal: string;
  model?: string;
  status: RunStatus | "queued" | "waiting_budget";
  startedAt?: string;
  updatedAt?: string;
  endedAt?: string;
  budget?: HarnessBudgetDTO;
  usageKnown: boolean;
  result?: unknown;
  errorCode?: string;
  errorDetail?: string;
  approval?: {
    checkpointID: string;
    toolCallID: string;
    toolKey: string;
    toolName: string;
    arguments: unknown;
  };
  cancelStatus?: "requested" | "failed" | "confirmed";
  cancelError?: string;
  cancelAttempt?: number;
};

export type HarnessTurnFeedEventDTO = {
  seq: number;
  turnID: string;
  type:
    | "turn.started"
    | "turn.waiting_input"
    | "turn.paused_budget"
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

// External/child waits retain the running lifecycle and never impersonate
// interaction or call-allowance pauses.
export function toolWaitPhase(snapshot: RunSnapshotDTO): "waiting_external_result" | "waiting_child" | "result_ready" | undefined {
  if (snapshot.run.status !== "running" || !snapshot.pendingTool) return undefined;
  if (snapshot.pendingTool.ready) return "result_ready";
  return snapshot.pendingTool.kind === "external_result" ? "waiting_external_result" : "waiting_child";
}
