import type {
  CancelWorkflowRunRequest,
  CompileWorkflowDefinitionRequest,
  PublishWorkflowDefinitionRequest,
  PublishWorkflowDefinitionResponse,
  ResolveWorkflowWaitRequest,
  RunSnapshotDTO,
  SetWorkflowDefinitionActivationRequest,
  SetWorkflowDefinitionActivationResponse,
  StartWorkflowRunRequest,
  WorkflowDefinitionHead,
  WorkflowDefinitionProposalReport,
  WorkflowDefinitionRevision,
  WorkflowDefinitionScopeKind,
	WorkflowTraceDTO,
} from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";
import { pathPart } from "./shared.js";

export const createWorkflowsCapability = (request: CapabilityRequest) => ({
  start: (payload: StartWorkflowRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>("/workflow-runs", { method: "POST", body: JSON.stringify(payload) }, options),
  resolveWait: (runID: string, payload: ResolveWorkflowWaitRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>(`/workflow-runs/${pathPart(runID)}/wait`, { method: "POST", body: JSON.stringify(payload) }, options),
  cancel: (runID: string, payload: CancelWorkflowRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>(
      `/workflow-runs/${pathPart(runID)}/cancel`,
      { method: "POST", body: JSON.stringify(payload) },
      options,
    ),
	trace: (runID: string, options?: RequestOptions) =>
		request<WorkflowTraceDTO>(`/workflow-runs/${pathPart(runID)}/trace`, {}, options),
  definitions: {
    compile: (payload: CompileWorkflowDefinitionRequest, options?: RequestOptions) =>
      request<WorkflowDefinitionProposalReport>(
        "/workflow-definitions/compile",
        { method: "POST", body: JSON.stringify(payload) },
        options,
      ),
    publish: (payload: PublishWorkflowDefinitionRequest, options?: RequestOptions) =>
      request<PublishWorkflowDefinitionResponse>(
        "/workflow-definitions",
        { method: "POST", body: JSON.stringify(payload) },
        options,
      ),
    list: (options?: RequestOptions) => request<WorkflowDefinitionHead[]>("/workflow-definitions", {}, options),
    get: (
      definitionID: string,
      revision: number,
      scope: WorkflowDefinitionScopeKind = "actor",
      options?: RequestOptions,
    ) => request<WorkflowDefinitionRevision>(
      `/workflow-definitions/${pathPart(definitionID)}/revisions/${revision}?scope=${encodeURIComponent(scope)}`,
      {},
      options,
    ),
    setActivation: (
      definitionID: string,
      payload: SetWorkflowDefinitionActivationRequest,
      options?: RequestOptions,
    ) => request<SetWorkflowDefinitionActivationResponse>(
      `/workflow-definitions/${pathPart(definitionID)}/activation`,
      { method: "POST", body: JSON.stringify(payload) },
      options,
    ),
  },
});
