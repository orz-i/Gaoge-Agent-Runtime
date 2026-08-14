import type { RunSnapshotDTO, StartTeamRunRequest } from "../types.js";
import type { RequestOptions } from "../runtime-client.js";
import type { CapabilityRequest } from "./shared.js";

export const createTeamsCapability = (request: CapabilityRequest) => ({
  start: (payload: StartTeamRunRequest, options?: RequestOptions) =>
    request<RunSnapshotDTO>("/team-runs", { method: "POST", body: JSON.stringify(payload) }, options),
});
