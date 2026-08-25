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

export type WorkflowValueSource =
  | { kind: "literal"; value: unknown; pointer?: string; nodeID?: never }
  | { kind: "workflow_input"; pointer?: string; value?: never; nodeID?: never }
  | { kind: "node_output" | "wait_response"; nodeID: string; pointer?: string; value?: never }
  | { kind: "map_item" | "map_index"; pointer?: string; value?: never; nodeID?: never };

export type WorkflowRetryPolicy = { maxAttempts?: number; retryableErrorCodes?: string[] };
export type WorkflowEffectClass =
  | "effect"
  | "agent.task"
  | "application.effect"
  | "media.effect"
  | "subworkflow"
  | "compensation";
export type WorkflowDefinitionReference = { id: string; revision?: number; hash?: string };
export type ExactWorkflowDefinitionReference = { id: string; revision: number; hash: string };
export type WorkflowEffectCall = {
  class: WorkflowEffectClass;
  kind: string;
  revision?: string;
  input: WorkflowValueSource;
  definition?: ExactWorkflowDefinitionReference;
  maxCostUnits?: number;
  retry?: WorkflowRetryPolicy;
};

type WorkflowNodeBase = { id: string; next?: string };
export type WorkflowNode =
  | (WorkflowNodeBase & {
      type: "effect";
      effect: {
        kind: string;
        input?: unknown;
        fromInput?: boolean;
        source?: WorkflowValueSource;
        maxCostUnits?: number;
        retry?: WorkflowRetryPolicy;
      };
    })
  | (WorkflowNodeBase & {
      type: "agent.task";
      agentTask: {
        agentKey: string;
        revision: string;
        input: WorkflowValueSource;
        maxCostUnits?: number;
        retry?: WorkflowRetryPolicy;
      };
    })
  | (WorkflowNodeBase & {
      type: "application.effect";
      applicationEffect: {
        capabilityKey: string;
        revision: string;
        input: WorkflowValueSource;
        maxCostUnits?: number;
        retry?: WorkflowRetryPolicy;
      };
    })
  | (WorkflowNodeBase & {
      type: "media.effect";
      mediaEffect: {
        capabilityKey: string;
        revision: string;
        input: WorkflowValueSource;
        maxCostUnits?: number;
        retry?: WorkflowRetryPolicy;
      };
    })
  | (WorkflowNodeBase & {
      type: "wait";
      wait: { kind: string; payload?: unknown; source?: WorkflowValueSource };
    })
  | (WorkflowNodeBase & {
      type: "if";
      if: { condition: WorkflowValueSource; thenNodeID: string; elseNodeID: string };
    })
  | (WorkflowNodeBase & {
      type: "parallel";
      parallel: { branches: Array<{ id: string; call: WorkflowEffectCall }>; maxConcurrency: number };
    })
  | (WorkflowNodeBase & {
      type: "map";
      map: { items: WorkflowValueSource; call: WorkflowEffectCall; maxConcurrency: number };
    })
  | (WorkflowNodeBase & {
      type: "subworkflow";
      subworkflow: {
        definition: ExactWorkflowDefinitionReference;
        input: WorkflowValueSource;
        maxCostUnits?: number;
        retry?: WorkflowRetryPolicy;
      };
    })
  | (WorkflowNodeBase & {
      type: "compensation";
      compensation: { do: WorkflowEffectCall; undo: WorkflowEffectCall };
    })
  | (WorkflowNodeBase & {
      type: "return";
      return: { value?: unknown; fromNode?: string; source?: WorkflowValueSource };
    });

export type WorkflowDefinitionPolicy = {
  requiredPermissions?: string[];
  costClass?: "none" | "low" | "medium" | "high" | "external_billing";
  maxCostUnits?: number;
  sideEffectClass?: "none" | "read" | "write" | "destructive";
};

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
	maxFanOut?: number;
	maxConcurrency?: number;
	maxNestedDepth?: number;
	maxAttemptsPerEffect?: number;
  };
	policy?: WorkflowDefinitionPolicy;
};

export type WorkflowDefinition = WorkflowDefinitionDraft & { hash: string };
export type WorkflowDefinitionScopeKind = "system" | "tenant" | "actor";
export type WorkflowDefinitionScope = {
  kind: WorkflowDefinitionScopeKind;
  tenantID?: string;
  actorID?: string;
};
export type WorkflowDefinitionHead = {
  scope: WorkflowDefinitionScope;
  definitionID: string;
  latestRevision: number;
  activeRevision?: number;
  availability: "active" | "disabled";
  version: number;
  updatedAt: string;
};
export type WorkflowDefinitionRevision = {
  scope: WorkflowDefinitionScope;
  definition: WorkflowDefinition;
  publishedBy: string;
  idempotencyKey: string;
  requestFingerprint: string;
  publishedAt: string;
};
export type WorkflowDefinitionDiff = {
  addedNodeIDs?: string[];
  removedNodeIDs?: string[];
  changedNodeIDs?: string[];
  inputSchemaChanged: boolean;
  outputSchemaChanged: boolean;
  limitsChanged: boolean;
};
export type WorkflowDefinitionPolicyImpact = {
  permissionsAdded?: string[];
  permissionsRemoved?: string[];
  costClassFrom: NonNullable<WorkflowDefinitionPolicy["costClass"]>;
  costClassTo: NonNullable<WorkflowDefinitionPolicy["costClass"]>;
  maxCostUnitsDelta: number;
  sideEffectFrom: NonNullable<WorkflowDefinitionPolicy["sideEffectClass"]>;
  sideEffectTo: NonNullable<WorkflowDefinitionPolicy["sideEffectClass"]>;
};
export type WorkflowDefinitionProposalReport = {
  definitionID: string;
  baseRevision: number;
  baseHash?: string;
  candidate: WorkflowDefinition;
  diff: WorkflowDefinitionDiff;
  impact: WorkflowDefinitionPolicyImpact;
};
export type CompileWorkflowDefinitionRequest = {
  scope: { kind: WorkflowDefinitionScopeKind };
  baseRevision?: number;
  draft: WorkflowDefinitionDraft;
};
export type PublishWorkflowDefinitionRequest = {
  scope: { kind: WorkflowDefinitionScopeKind };
  draft: WorkflowDefinitionDraft;
  expectedRevision?: number;
  mode?: "activate" | "stage";
  idempotencyKey?: string;
};
export type PublishWorkflowDefinitionResponse = {
  revision: WorkflowDefinitionRevision;
  head: WorkflowDefinitionHead;
  reused: boolean;
};
export type SetWorkflowDefinitionActivationRequest = {
  scope: { kind: WorkflowDefinitionScopeKind };
  targetRevision?: number;
  availability: "active" | "disabled";
  expectedVersion: number;
};
export type SetWorkflowDefinitionActivationResponse = { head: WorkflowDefinitionHead; reused: boolean };

export type StartWorkflowRunRequest = {
  thread: ThreadRefDTO;
  input: unknown;
  clientRunID?: string;
  goal: string;
	definition?: WorkflowDefinitionDraft;
	definitionReference?: WorkflowDefinitionReference;
};

export type ResolveWorkflowWaitRequest = { expectedRevision: number; response: unknown };
export type CancelWorkflowRunRequest = { expectedRevision: number; reason?: string };
export type WorkflowTraceDTO = {
  runID: string;
  status: RunStatus;
  revision: number;
  definition: ExactWorkflowDefinitionReference;
  currentNodeID?: string;
  nestedDepth: number;
  budget: {
    nodeActivations: number;
    effects: number;
    segments: number;
    stateBytes: number;
    costUnitsUsed: number;
    costUnitsReserved: number;
  };
  activations: Array<{
    id: string;
    nodeID: string;
    nodeType: WorkflowNode["type"];
    status: "running" | "waiting" | "completed" | "failed";
    attempt: number;
    effectIDs?: string[];
    waitID?: string;
    errorCode?: string;
  }>;
  effects: Array<{
    id: string;
    nodeID: string;
    class: WorkflowEffectClass;
    kind: string;
    revision?: string;
    status: "pending" | "completed" | "failed";
    attempt: number;
    maxAttempts: number;
    childRunID?: string;
    receiptID?: string;
    costUnits: number;
    maxCostUnits: number;
    compensation?: boolean;
    errorCode?: string;
  }>;
  waits: Array<{ id: string; nodeID: string; kind: string; status: "pending" | "resolved" }>;
  compensations: Array<{
    id: string;
    nodeID: string;
    kind: string;
    status: "pending" | "running" | "completed" | "failed";
    effectID?: string;
    receiptID?: string;
    errorCode?: string;
  }>;
  events: Array<{ seq: number; type: string; createdAt: string }>;
};

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
