import type { RunSnapshotDTO, StartAgentRunRequest } from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";

export const createAgentCapability = (request: CapabilityRequest) => ({
  start: (payload: StartAgentRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>("/agent-runs", { method: "POST", body: JSON.stringify(payload) }, options),
});
