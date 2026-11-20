import type { RunSnapshotDTO, StartAgentRunRequest } from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";

export const createAgentCapability = (request: CapabilityRequest) => ({
  start: (payload: StartAgentRunRequest, options?: RequestOptions) => {
    const ceiling = payload.maxModelOutputTokens;
    if (ceiling !== undefined && (!Number.isSafeInteger(ceiling) || ceiling < 0)) {
      throw new TypeError("maxModelOutputTokens must be a non-negative safe integer");
    }
    return request<RunSnapshotDTO>("/agent-runs", { method: "POST", body: JSON.stringify(payload) }, options);
  },
});
