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
  it("uses only the ten target Runtime endpoints", async () => {
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(
      url.includes("/feed?")
        ? new Response(`id: 1\ndata: {"seq":1,"runID":"run/1","type":"run.completed","terminal":true,"createdAt":"2026-08-06T00:00:01Z"}\n\n`, {
            headers: { "content-type": "text/event-stream" },
          })
        : json(snapshot),
    ));
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
    for await (const event of client.runs.feed("run/1", { reconnectDelayMS: 0 })) {
      expect(event.terminal).toBe(true);
    }
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
      "https://runtime.test/api/v1/runs/run%2F1/feed?afterSeq=0",
    ]);
  });

  it("returns stable API errors", async () => {
    const fetcher = vi.fn().mockResolvedValue(json({ error: { code: "run.conflict", message: "revision conflict", requestID: "request-1" } }, 409));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    await expect(client.runs.get("run-1")).rejects.toEqual(expect.objectContaining<Partial<RuntimeAPIError>>({
      status: 409, code: "run.conflict", requestID: "request-1",
    }));
  });

  it("reconnects the Run Feed from the last sequence without duplicates", async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(
        `id: 1\r\ndata: {"seq":1,"runID":"run-1","type":"model.delta","delta":"hel","createdAt":"2026-08-06T00:00:00Z"}\r\n\r\n`,
      ))
      .mockResolvedValueOnce(new Response(
        `id: 2\ndata: {"seq":2,"runID":"run-1","type":"run.completed","terminal":true,"createdAt":"2026-08-06T00:00:01Z"}\n\n`,
      ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    const events = [];
    for await (const event of client.runs.feed("run-1", { reconnectDelayMS: 0 })) events.push(event);
    expect(events.map((event) => [event.seq, event.type])).toEqual([
      [1, "model.delta"],
      [2, "run.completed"],
    ]);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/runs/run-1/feed?afterSeq=0",
      "https://runtime.test/api/v1/runs/run-1/feed?afterSeq=1",
    ]);
  });

  it("restores the Run snapshot before continuing after an expired feed cursor", async () => {
    const runningSnapshot = {
      ...snapshot,
      run: { ...snapshot.run, status: "running", revision: 4 },
    };
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(
        JSON.stringify({ error: { code: "runfeed.cursor_expired", message: "expired" } }),
        { status: 409, headers: { "content-type": "application/json", "x-run-feed-head": "7" } },
      ))
      .mockResolvedValueOnce(json(runningSnapshot))
      .mockResolvedValueOnce(new Response(
        `id: 8\ndata: {"seq":8,"runID":"run-1","type":"run.completed","terminal":true,"createdAt":"2026-08-17T00:16:01Z"}\n\n`,
        { headers: { "content-type": "text/event-stream" } },
      ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    const restored = vi.fn();
    const events = [];

    for await (const event of client.runs.feed("run-1", {
      afterSeq: 2,
      reconnectDelayMS: 0,
      onCursorExpired: restored,
    })) events.push(event);

    expect(restored).toHaveBeenCalledWith(runningSnapshot);
    expect(events.map((event) => event.seq)).toEqual([8]);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/runs/run-1/feed?afterSeq=2",
      "https://runtime.test/api/v1/runs/run-1",
      "https://runtime.test/api/v1/runs/run-1/feed?afterSeq=7",
    ]);
  });

  it("never skips an expired Run cursor without a snapshot recovery callback", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: "runfeed.cursor_expired", message: "expired" } }),
      { status: 409, headers: { "content-type": "application/json", "x-run-feed-head": "7" } },
    ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });

    const iterator = client.runs.feed("run-1", { afterSeq: 2, reconnectDelayMS: 0 });
    await expect(iterator.next()).rejects.toEqual(expect.objectContaining<Partial<RuntimeAPIError>>({
      status: 409,
      code: "runfeed.cursor_expired",
    }));
  });

  it("uses Harness Turn identity for snapshot, semantic feed, and approval", async () => {
    const harnessCommands = [{
      id: "workflow", trigger: "/workflow", title: "Workflow", capabilityKey: "runtime.workflow",
      definitionVersion: "v1", executionClass: "workflow", source: "first_party", inputSchema: { type: "object" },
    }];
    const harnessSnapshot = {
      turn: {
        id: "ht/1", hostTurn: { kind: "conversation_turn", id: "client-turn-1" },
        status: "completed", revision: 3,
        createdAt: "2026-08-17T00:00:00Z", updatedAt: "2026-08-17T00:00:01Z",
      },
      invocations: [{
        id: "hiv-1", turnID: "ht/1", capabilityKey: "runtime.agent", executionClass: "agent",
        status: "completed", attempt: 1, outputRefs: [], revision: 2,
        createdAt: "2026-08-17T00:00:00Z", updatedAt: "2026-08-17T00:00:01Z",
      }],
      interactions: [],
      items: [],
      output: { contentType: "text", content: "done" },
    };
    const fetcher = vi.fn()
      .mockResolvedValueOnce(json(harnessCommands))
      .mockResolvedValueOnce(json(harnessSnapshot))
      .mockResolvedValueOnce(new Response(
        `id: 1\ndata: {"seq":1,"turnID":"ht/1","type":"item.delta","itemID":"message-1","itemKind":"agent_message","delta":"hi","createdAt":"2026-08-17T00:00:00Z"}\n\n` +
        `id: 2\ndata: {"seq":2,"turnID":"ht/1","type":"turn.completed","status":"completed","terminal":true,"createdAt":"2026-08-17T00:00:01Z"}\n\n`,
        { headers: { "content-type": "text/event-stream" } },
      ))
      .mockResolvedValueOnce(json(harnessSnapshot))
      .mockResolvedValueOnce(json(harnessSnapshot));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });

    await expect(client.harness.commands.list()).resolves.toEqual(harnessCommands);
    await expect(client.harness.turns.get("ht/1")).resolves.toEqual(harnessSnapshot);
    const events = [];
    for await (const event of client.harness.turns.feed("ht/1", { reconnectDelayMS: 0 })) events.push(event);
    await client.harness.turns.resolveApproval("ht/1", "approve", "continue");
    await client.harness.turns.resolveInteraction("ht/1", "interaction/1", { candidateID: "candidate-2" });

    expect(events.map((event) => [event.seq, event.type, event.itemID])).toEqual([
      [1, "item.delta", "message-1"],
      [2, "turn.completed", undefined],
    ]);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/harness/commands",
      "https://runtime.test/api/v1/harness/turns/ht%2F1",
      "https://runtime.test/api/v1/harness/turns/ht%2F1/feed?afterSeq=0",
      "https://runtime.test/api/v1/harness/turns/ht%2F1/approval",
      "https://runtime.test/api/v1/harness/turns/ht%2F1/interactions/interaction%2F1",
    ]);
    expect(fetcher.mock.calls.some((call) => String(call[0]).includes("/runs/"))).toBe(false);
  });

  it("restores a Harness Turn snapshot before continuing after an expired feed cursor", async () => {
    const harnessSnapshot = {
      turn: {
        id: "ht-1", hostTurn: { kind: "conversation_turn", id: "client-turn-1" },
        status: "waiting_input", revision: 4,
        createdAt: "2026-08-17T00:00:00Z", updatedAt: "2026-08-17T00:16:00Z",
      },
      invocations: [{
        id: "hiv-1", turnID: "ht-1", capabilityKey: "runtime.agent", executionClass: "agent",
        status: "waiting_input", attempt: 1, outputRefs: [], revision: 2,
        createdAt: "2026-08-17T00:00:00Z", updatedAt: "2026-08-17T00:16:00Z",
      }],
      interactions: [],
      items: [],
    };
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(
        JSON.stringify({ error: { code: "harness.feed_cursor_expired", message: "expired" } }),
        { status: 409, headers: { "content-type": "application/json", "x-harness-feed-head": "7" } },
      ))
      .mockResolvedValueOnce(json(harnessSnapshot))
      .mockResolvedValueOnce(new Response(
        `id: 8\ndata: {"seq":8,"turnID":"ht-1","type":"turn.completed","status":"completed","terminal":true,"createdAt":"2026-08-17T00:16:01Z"}\n\n`,
        { headers: { "content-type": "text/event-stream" } },
      ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    const restored = vi.fn();
    const events = [];

    for await (const event of client.harness.turns.feed("ht-1", {
      afterSeq: 2,
      reconnectDelayMS: 0,
      onCursorExpired: restored,
    })) events.push(event);

    expect(restored).toHaveBeenCalledWith(harnessSnapshot);
    expect(events.map((event) => event.seq)).toEqual([8]);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/harness/turns/ht-1/feed?afterSeq=2",
      "https://runtime.test/api/v1/harness/turns/ht-1",
      "https://runtime.test/api/v1/harness/turns/ht-1/feed?afterSeq=7",
    ]);
  });

  it("never skips an expired Harness cursor without a snapshot recovery callback", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: "harness.feed_cursor_expired", message: "expired" } }),
      { status: 409, headers: { "content-type": "application/json", "x-harness-feed-head": "7" } },
    ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });

    const iterator = client.harness.turns.feed("ht-1", { afterSeq: 2, reconnectDelayMS: 0 });
    await expect(iterator.next()).rejects.toEqual(expect.objectContaining<Partial<RuntimeAPIError>>({
      status: 409,
      code: "harness.feed_cursor_expired",
    }));
  });
});
