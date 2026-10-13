import { describe, expect, it } from "vitest";
import { toolWaitPhase, type RunSnapshotDTO } from "./types.js";

describe("tool wait projection", () => {
  const base: RunSnapshotDTO = {
    run: { id: "wait-run", kind: "agent", actor: { tenantID: "tenant", actorID: "actor" }, thread: { kind: "test", id: "thread" }, goal: "compute", status: "running", revision: 3, createdAt: "", updatedAt: "" },
    state: {}, eventHead: 2,
  };
  it("preserves running for external and child waits", () => {
    const pendingTool = { kind: "external_result" as const, callID: "call", toolKey: "test.external", ready: false };
    expect(toolWaitPhase({ ...base, pendingTool })).toBe("waiting_external_result");
    expect(toolWaitPhase({ ...base, pendingTool: { ...pendingTool, kind: "child_terminal" } })).toBe("waiting_child");
    expect(toolWaitPhase({ ...base, pendingTool: { ...pendingTool, ready: true } })).toBe("result_ready");
    expect(base.run.status).toBe("running");
  });
  it("does not revive a cancelled, completed or paused run using stale wait metadata", () => {
    for (const status of ["cancelled", "completed", "paused_budget", "waiting_input"] as const) {
      expect(toolWaitPhase({ ...base, run: { ...base.run, status }, pendingTool: { kind: "external_result", callID: "call", toolKey: "test.external", ready: true } })).toBeUndefined();
    }
    expect(toolWaitPhase(base)).toBeUndefined();
  });
});
