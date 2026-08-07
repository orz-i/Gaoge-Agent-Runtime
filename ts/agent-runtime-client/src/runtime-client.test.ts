import { describe, expect, it, vi } from "vitest";

import { RuntimeAPIError, RuntimeClient } from "./runtime-client";

const snapshot = {
  run: {
    id: "run-1", kind: "agent", actor: { tenantID: "default", actorID: "1" },
    thread: { kind: "conversation", id: "thread-1" }, goal: "Answer", status: "completed",
    revision: 2, createdAt: "2026-08-06T00:00:00Z", updatedAt: "2026-08-06T00:00:01Z",
  },
  state: {}, events: [],
};

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {
  status,
  headers: { "content-type": "application/json" },
});

describe("RuntimeClient target API", () => {
  it("uses only the nine target Runtime endpoints", async () => {
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(json(snapshot)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    await client.agent.start({ thread: { kind: "conversation", id: "thread-1" }, input: { content: "Answer" } });
    await client.plans.start({ thread: { kind: "conversation", id: "thread-1" }, input: { content: "Plan" } });
    await client.plans.approve("plan/1", { expectedRevision: 2, decision: "approve" });
    await client.workflows.start({ thread: { kind: "conversation", id: "thread-1" }, input: {}, goal: "Flow", definition: { id: "flow", revision: 1, name: "Flow", nodes: [{ id: "return", type: "return", return: { value: {} } }] } });
    await client.workflows.resolveWait("workflow/1", { expectedRevision: 2, response: {} });
    await client.teams.start({ thread: { kind: "conversation", id: "thread-1" }, goal: "Team", mode: "parallel", members: [{ id: "one", goal: "One" }], join: { mode: "all" } });
    await client.runs.get("run/1");
    await client.runs.cancel("run/1", { expectedRevision: 2, reason: "stop" });
    await client.runs.workbench("run/1");
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/agent-runs",
      "https://runtime.test/api/v1/plan-runs",
      "https://runtime.test/api/v1/plan-runs/plan%2F1/approval",
      "https://runtime.test/api/v1/workflow-runs",
      "https://runtime.test/api/v1/workflow-runs/workflow%2F1/wait",
      "https://runtime.test/api/v1/team-runs",
      "https://runtime.test/api/v1/runs/run%2F1",
      "https://runtime.test/api/v1/runs/run%2F1/cancel",
      "https://runtime.test/api/v1/runs/run%2F1/workbench",
    ]);
  });

  it("returns stable API errors", async () => {
    const fetcher = vi.fn().mockResolvedValue(json({ error: { code: "run.conflict", message: "revision conflict", requestID: "request-1" } }, 409));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    await expect(client.runs.get("run-1")).rejects.toEqual(expect.objectContaining<Partial<RuntimeAPIError>>({
      status: 409, code: "run.conflict", requestID: "request-1",
    }));
  });
});
