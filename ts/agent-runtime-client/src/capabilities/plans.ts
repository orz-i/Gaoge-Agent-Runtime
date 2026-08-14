import type { ResolvePlanApprovalRequest, RunSnapshotDTO, StartPlanRunRequest } from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";
import { pathPart } from "./shared.js";

export const createPlansCapability = (request: CapabilityRequest) => ({
  start: (payload: StartPlanRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>("/plan-runs", { method: "POST", body: JSON.stringify(payload) }, options),
  approve: (runID: string, payload: ResolvePlanApprovalRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>(`/plan-runs/${pathPart(runID)}/approval`, { method: "POST", body: JSON.stringify(payload) }, options),
});
