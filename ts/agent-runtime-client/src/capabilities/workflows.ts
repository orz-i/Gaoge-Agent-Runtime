import type { ResolveWorkflowWaitRequest, RunSnapshotDTO, StartWorkflowRunRequest } from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";
import { pathPart } from "./shared.js";

export const createWorkflowsCapability = (request: CapabilityRequest) => ({
  start: (payload: StartWorkflowRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>("/workflow-runs", { method: "POST", body: JSON.stringify(payload) }, options),
  resolveWait: (runID: string, payload: ResolveWorkflowWaitRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>(`/workflow-runs/${pathPart(runID)}/wait`, { method: "POST", body: JSON.stringify(payload) }, options),
});
