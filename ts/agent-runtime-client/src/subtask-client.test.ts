import { expect, it, vi } from "vitest";
import { RuntimeClient } from "./runtime-client";

it("addresses subtask cancellation and checkpoint approval inside the authorized Turn", async () => {
  const body = { turn: { id: "turn/1" }, budget: { scopeID: "turn/1", revision: 3 }, subtasks: [{ id: "delegation/1", status: "cancelled" }] };
  const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } })));
  const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
  expect(await client.harness.turns.cancelSubtask("turn/1", "delegation/1", "stop")).toEqual(body);
  await client.harness.turns.resolveSubtaskApproval("turn/1", "delegation/1", "checkpoint-2", "approve", "checked");
  expect(fetcher.mock.calls.map(([url, init]) => [url, init.method, JSON.parse(init.body)])).toEqual([
    ["https://runtime.test/api/v1/harness/turns/turn%2F1/subtasks/delegation%2F1/cancel", "POST", { reason: "stop" }],
    ["https://runtime.test/api/v1/harness/turns/turn%2F1/subtasks/delegation%2F1/approval", "POST", { checkpointID: "checkpoint-2", decision: "approve", comment: "checked" }],
  ]);
});
