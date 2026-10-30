import { getEventListeners } from "node:events";
import { createServer } from "node:http";

import { describe, expect, it, vi } from "vitest";

import { RuntimeAPIError, RuntimeClient } from "./runtime-client";

const snapshot = {
  run: {
    id: "run-1", kind: "agent", actor: { tenantID: "default", actorID: "1" },
    thread: { kind: "conversation", id: "thread-1" }, goal: "Answer", status: "completed",
    revision: 2, createdAt: "2026-08-06T00:00:00Z", updatedAt: "2026-08-06T00:00:01Z",
  },
  state: {}, eventHead: 0,
};

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {
  status,
  headers: { "content-type": "application/json" },
});

describe("RuntimeClient target API", () => {
  it("uses bounded owner-scoped Harness status and Timeline endpoints", async () => {
    const committed = {
      turn: { id: "ht/1", hostTurn: { kind: "conversation_turn", id: "turn" },
        status: "running", revision: 3, createdAt: "2026-10-26T00:00:00Z", updatedAt: "2026-10-26T00:00:01Z" },
    };
    const timeline = { items: [{ id: "item-5", turnID: "ht/1", seq: 5,
      kind: "tool", status: "completed", createdAt: "2026-10-26T00:00:00Z",
      updatedAt: "2026-10-26T00:00:01Z" }], nextAfterSeq: 5 };
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json(committed))
      .mockResolvedValueOnce(json(timeline));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    await expect(client.harness.turns.state("ht/1")).resolves.toEqual(committed);
    await expect(client.harness.turns.items("ht/1", { afterSeq: 4, limit: 1 })).resolves.toEqual(timeline);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/harness/turns/ht%2F1/state",
      "https://runtime.test/api/v1/harness/turns/ht%2F1/items?afterSeq=4&limit=1",
    ]);
  });

  it("requires an exact Harness approval checkpoint and never posts a missing binding", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({}));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    const controller = new AbortController();
    await client.harness.turns.resolveApproval("ht/1", "checkpoint/1", "reject", "Use the current evidence", { signal: controller.signal });
    expect(JSON.parse(String(fetcher.mock.calls[0]?.[1]?.body))).toEqual({
      checkpointID: "checkpoint/1", decision: "reject", comment: "Use the current evidence",
    });
    expect(fetcher.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
    for (const invalid of ["", " ", " checkpoint/1", "x".repeat(129)]) {
      expect(() => client.harness.turns.resolveApproval("ht/1", invalid, "approve")).toThrow("checkpointID");
    }
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("uses only published Runtime endpoints", async () => {
    const fetcher = vi.fn().mockImplementation((url: string) => Promise.resolve(
      url.includes("/feed?")
        ? new Response(`id: 1\ndata: {"seq":1,"runID":"run/1","type":"run.completed","terminal":true,"createdAt":"2026-08-06T00:00:01Z"}\n\n`, {
            headers: { "content-type": "text/event-stream" },
          })
        : json(snapshot),
    ));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    await client.agent.start({ thread: { kind: "conversation", id: "thread-1" }, input: { content: "Answer" } });
    await client.workflows.start({ thread: { kind: "conversation", id: "thread-1" }, input: {}, goal: "Flow", definition: { id: "flow", revision: 1, name: "Flow", nodes: [{ id: "return", type: "return", return: { value: {} } }] } });
    await client.workflows.resolveWait("workflow/1", { expectedRevision: 2, response: {} });
    await client.workflows.cancel("workflow/1", { expectedRevision: 3, reason: "stop and compensate" });
    await client.workflows.trace("workflow/1");
    await client.teams.start({ thread: { kind: "conversation", id: "thread-1" }, goal: "Team", mode: "parallel", members: [{ id: "one", goal: "One" }], join: { mode: "all" } });
    await client.runs.get("run/1");
    await client.runs.events("run/1", { afterSeq: 4, limit: 25 });
    await client.runs.cancel("run/1", { expectedRevision: 2, reason: "stop" });
    await client.runs.workbench("run/1");
    for await (const event of client.runs.feed("run/1", { reconnectDelayMS: 0 })) {
      expect(event.terminal).toBe(true);
    }
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/agent-runs",
      "https://runtime.test/api/v1/workflow-runs",
      "https://runtime.test/api/v1/workflow-runs/workflow%2F1/wait",
      "https://runtime.test/api/v1/workflow-runs/workflow%2F1/cancel",
      "https://runtime.test/api/v1/workflow-runs/workflow%2F1/trace",
      "https://runtime.test/api/v1/team-runs",
      "https://runtime.test/api/v1/runs/run%2F1",
      "https://runtime.test/api/v1/runs/run%2F1/events?afterSeq=4&limit=25",
      "https://runtime.test/api/v1/runs/run%2F1/cancel",
      "https://runtime.test/api/v1/runs/run%2F1/workbench",
      "https://runtime.test/api/v1/runs/run%2F1/feed?afterSeq=0",
    ]);
  });

  it("round-trips immutable Workflow definition registry contracts", async () => {
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(json({})));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });
    const draft = {
      id: "story.shot-plan.v2",
      revision: 1,
      name: "Shot Plan",
      nodes: [{ id: "done", type: "return" as const, return: { value: { status: "ready" } } }],
      policy: { costClass: "none" as const, maxCostUnits: 0, sideEffectClass: "none" as const },
    };

    await client.workflows.definitions.compile({ scope: { kind: "actor" }, draft });
    await client.workflows.definitions.publish({
      scope: { kind: "tenant" }, draft, expectedRevision: 0, idempotencyKey: "publish-1",
    });
    await client.workflows.definitions.list();
    await client.workflows.definitions.get("story.shot-plan/v2", 2, "tenant");
    await client.workflows.definitions.setActivation("story.shot-plan/v2", {
      scope: { kind: "tenant" }, availability: "active", targetRevision: 2, expectedVersion: 3,
    });
    await client.workflows.start({
      thread: { kind: "conversation", id: "thread-1" }, input: { storyID: "story-1" },
      goal: "compile shot plan", definitionReference: { id: "story.shot-plan.v2" },
    });

    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      "https://runtime.test/api/v1/workflow-definitions/compile",
      "https://runtime.test/api/v1/workflow-definitions",
      "https://runtime.test/api/v1/workflow-definitions",
      "https://runtime.test/api/v1/workflow-definitions/story.shot-plan%2Fv2/revisions/2?scope=tenant",
      "https://runtime.test/api/v1/workflow-definitions/story.shot-plan%2Fv2/activation",
      "https://runtime.test/api/v1/workflow-runs",
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
      .mockResolvedValueOnce(json(harnessSnapshot))
      .mockResolvedValueOnce(json(harnessSnapshot))
      .mockResolvedValueOnce(json(harnessSnapshot));
    const client = new RuntimeClient({ baseURL: "https://runtime.test/api/v1", fetch: fetcher });

    await expect(client.harness.commands.list()).resolves.toEqual(harnessCommands);
    await expect(client.harness.turns.get("ht/1")).resolves.toEqual(harnessSnapshot);
    const events = [];
    for await (const event of client.harness.turns.feed("ht/1", { reconnectDelayMS: 0 })) events.push(event);
    await client.harness.turns.resolveApproval("ht/1", "checkpoint-1", "approve", "continue");
    await client.harness.turns.resolveInteraction("ht/1", "interaction/1", { candidateID: "candidate-2" });
    await client.harness.turns.retryInvocation("ht/1", "invocation/1");
    await client.harness.turns.grantCallAllowance("ht/1", 7, { modelCalls: 2 });

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
      "https://runtime.test/api/v1/harness/turns/ht%2F1/invocations/invocation%2F1/retry",
      "https://runtime.test/api/v1/harness/turns/ht%2F1/allowance",
    ]);
    expect(JSON.parse(String(fetcher.mock.calls[6]?.[1]?.body))).toEqual({
      expectedTurnRevision: 7, modelCalls: 2,
    });
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

type FeedKind = "run" | "harness";
const sseFrame = (kind: FeedKind, seq: number, terminal = false) => `data: ${JSON.stringify({
  seq, ...(kind === "run" ? { runID: "run-1" } : { turnID: "ht-1" }),
  type: terminal ? "completed" : "delta", terminal,
})}\n\n`;

function feedFor(client: RuntimeClient, kind: FeedKind, options: {
  signal?: AbortSignal; reconnectDelayMS?: number; maxReconnects?: number;
} = {}) {
  return kind === "run"
    ? client.runs.feed("run-1", options)
    : client.harness.turns.feed("ht-1", options);
}

describe.each(["run", "harness"] as const)("%s feed resilience", (kind) => {
  it("recovers from a body read failure at the last delivered cursor without duplicates", async () => {
    let firstController!: ReadableStreamDefaultController<Uint8Array>;
    const firstBody = new ReadableStream<Uint8Array>({
      start(controller) {
        firstController = controller;
        controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1)));
      },
    });
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(firstBody))
      .mockResolvedValueOnce(new Response(sseFrame(kind, 1) + sseFrame(kind, 2, true)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0, maxReconnects: 2 });

    const first = await iterator.next();
    expect(first.value?.seq).toBe(1);
    firstController.error(new Error("connection reset during reader.read"));
    const second = await iterator.next();
    expect(second.value?.seq).toBe(2);
    expect(second.value?.terminal).toBe(true);
    expect((await iterator.next()).done).toBe(true);
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual([
      expect.stringContaining("feed?afterSeq=0"),
      expect.stringContaining("feed?afterSeq=1"),
    ]);
  });

  it("limits retries for repeated body read failures", async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.error(new Error("reset on read")); },
    })));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0, maxReconnects: 2 });

    await expect(iterator.next()).rejects.toThrow("reset on read");
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("honors a zero reconnect budget after a nonterminal EOF", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(sseFrame(kind, 1)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0, maxReconnects: 0 });

    expect((await iterator.next()).value?.seq).toBe(1);
    await expect(iterator.next()).rejects.toEqual(expect.objectContaining<Partial<RuntimeAPIError>>({
      status: 0,
      code: kind === "run" ? "runfeed.disconnected" : "harness.feed_disconnected",
    }));
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("does not retry a malformed SSE payload as a network failure", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("data: {invalid-json}\n\n"));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });

    await expect(feedFor(client, kind, { reconnectDelayMS: 0, maxReconnects: 2 }).next())
      .rejects.toBeInstanceOf(SyntaxError);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("cancels the reader and active fetch when the caller returns early", async () => {
    const cancelled = vi.fn();
    let fetchSignal: AbortSignal | undefined;
    const fetcher = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
      fetchSignal = init?.signal ?? undefined;
      return new Response(new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
        cancel: cancelled,
      }));
    });
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0 });

    expect((await iterator.next()).value?.seq).toBe(1);
    await iterator.return(undefined);
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(fetchSignal?.aborted).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("stops cleanly without reconnecting when the caller aborts during a read", async () => {
    const abortController = new AbortController();
    let fetchSignal: AbortSignal | undefined;
    const fetcher = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
      fetchSignal = init?.signal ?? undefined;
      return new Response(new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1)));
          fetchSignal?.addEventListener("abort", () => controller.error(new DOMException("aborted", "AbortError")), { once: true });
        },
      }));
    });
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { signal: abortController.signal, reconnectDelayMS: 0 });

    expect((await iterator.next()).value?.seq).toBe(1);
    abortController.abort();
    expect((await iterator.next()).done).toBe(true);
    expect(fetchSignal?.aborted).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});

// A stalled network reader or cancel hook must not prevent explicit iterator shutdown.
async function settlePromptly(promise: Promise<unknown>, timeoutMS = 350): Promise<unknown> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error("SSE iterator did not settle promptly")), timeoutMS);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

describe.each(["run", "harness"] as const)("%s feed shutdown audit regressions", (kind) => {
  it("return interrupts a pending next/read and closes the fetch without an external signal", async () => {
    const cancelled = vi.fn();
    let fetchSignal: AbortSignal | undefined;
    const fetcher = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
      fetchSignal = init?.signal ?? undefined;
      return new Response(new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
        cancel: cancelled,
      }));
    });
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0 });
    expect((await iterator.next()).value?.seq).toBe(1);

    const pendingNext = iterator.next();
    await Promise.resolve();
    expect(await settlePromptly(iterator.return(undefined))).toEqual({ done: true, value: undefined });
    expect(await settlePromptly(pendingNext)).toEqual({ done: true, value: undefined });
    expect(fetchSignal?.aborted).toBe(true);
    expect(cancelled).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("does not wait indefinitely when the stream cancel hook never completes", async () => {
    const cancelled = vi.fn(() => new Promise<void>(() => {}));
    let fetchSignal: AbortSignal | undefined;
    const fetcher = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
      fetchSignal = init?.signal ?? undefined;
      return new Response(new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
        cancel: cancelled,
      }));
    });
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { reconnectDelayMS: 0 });
    expect((await iterator.next()).value?.seq).toBe(1);

    expect(await settlePromptly(iterator.return(undefined))).toEqual({ done: true, value: undefined });
    expect(fetchSignal?.aborted).toBe(true);
    expect(cancelled).toHaveBeenCalledTimes(1);
  });

  it("does not leak rejected cancel hooks into an unhandled rejection", async () => {
    const cancelled = vi.fn(() => Promise.reject(new Error("cancel failed")));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: async () => new Response(
      new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
        cancel: cancelled,
      }),
    ) });
    const iterator = feedFor(client, kind);
    expect((await iterator.next()).value?.seq).toBe(1);
    expect(await settlePromptly(iterator.return(undefined))).toEqual({ done: true, value: undefined });
    expect(cancelled).toHaveBeenCalledTimes(1);
  });

  it("never delivers already buffered events after external abort", async () => {
    const abort = new AbortController();
    const fetcher = vi.fn().mockResolvedValue(new Response(sseFrame(kind, 1) + sseFrame(kind, 2, true)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { signal: abort.signal, reconnectDelayMS: 0 });
    expect((await iterator.next()).value?.seq).toBe(1);
    abort.abort();
    expect(await settlePromptly(iterator.next())).toEqual({ done: true, value: undefined });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});

describe.each(["run", "harness"] as const)("%s feed real HTTP shutdown", (kind) => {
  it("closes an idle HTTP SSE connection when return interrupts next", async () => {
    let signal: AbortSignal | undefined;
    let markClosed!: () => void;
    const responseClosed = new Promise<void>((resolve) => { markClosed = resolve; });
    const server = createServer((_request, response) => {
      response.writeHead(200, { "content-type": "text/event-stream" });
      response.write(sseFrame(kind, 1));
      response.on("close", markClosed);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("invalid test server address");
      const client = new RuntimeClient({
        baseURL: `http://127.0.0.1:${address.port}`,
        fetch: async (url, init) => {
          signal = init?.signal ?? undefined;
          return fetch(url, init);
        },
      });
      const iterator = feedFor(client, kind, { reconnectDelayMS: 0 });
      expect((await iterator.next()).value?.seq).toBe(1);

      const pendingNext = iterator.next();
      await Promise.resolve();
      expect(await settlePromptly(iterator.return(undefined), 1000)).toEqual({ done: true, value: undefined });
      expect(await settlePromptly(pendingNext, 1000)).toEqual({ done: true, value: undefined });
      await settlePromptly(responseClosed, 1000);
      expect(signal?.aborted).toBe(true);
    } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });
});

describe.each(["run", "harness"] as const)("%s feed lazy abort listener lifecycle", (kind) => {
  const listeners = (signal: AbortSignal) => getEventListeners(signal, "abort").length;

  it("does not attach listeners or fetch for unconsumed feeds, even when many share a signal", async () => {
    const abort = new AbortController();
    const fetcher = vi.fn().mockResolvedValue(new Response(sseFrame(kind, 1, true)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterators = Array.from({ length: 100 }, () => feedFor(client, kind, { signal: abort.signal }));

    const beforeClose = listeners(abort.signal);
    await Promise.all(iterators.map((iterator) => iterator.return(undefined)));
    expect(beforeClose).toBe(0);
    expect(listeners(abort.signal)).toBe(0);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("subscribes exactly once on the first next and cleans up on early return", async () => {
    const abort = new AbortController();
    const fetcher = vi.fn().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
    })));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { signal: abort.signal });
    expect(listeners(abort.signal)).toBe(0);

    const first = iterator.next();
    expect(listeners(abort.signal)).toBe(1);
    expect((await first).value?.seq).toBe(1);
    const pending = iterator.next();
    expect(listeners(abort.signal)).toBe(1);
    expect(await settlePromptly(iterator.return(undefined))).toEqual({ done: true, value: undefined });
    expect(await settlePromptly(pending)).toEqual({ done: true, value: undefined });
    expect(listeners(abort.signal)).toBe(0);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("cleans up after terminal completion and after malformed stream errors", async () => {
    for (const payload of [sseFrame(kind, 1, true), "data: {malformed-json}\n\n"]) {
      const abort = new AbortController();
      const fetcher = vi.fn().mockResolvedValue(new Response(payload));
      const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
      const iterator = feedFor(client, kind, { signal: abort.signal, reconnectDelayMS: 0 });
      expect(listeners(abort.signal)).toBe(0);
      if (payload.includes("malformed-json")) {
        await expect(iterator.next()).rejects.toBeInstanceOf(SyntaxError);
      } else {
        expect((await iterator.next()).value?.terminal).toBe(true);
        expect((await iterator.next()).done).toBe(true);
      }
      expect(listeners(abort.signal)).toBe(0);
      expect(fetcher).toHaveBeenCalledTimes(1);
    }
  });

  it("does not subscribe or fetch when already aborted before the first next", async () => {
    const abort = new AbortController();
    const fetcher = vi.fn().mockResolvedValue(new Response(sseFrame(kind, 1)));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { signal: abort.signal });
    abort.abort();
    expect(await iterator.next()).toEqual({ done: true, value: undefined });
    expect(listeners(abort.signal)).toBe(0);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("removes the active listener immediately when aborted", async () => {
    const abort = new AbortController();
    const fetcher = vi.fn().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(kind, 1))); },
    })));
    const client = new RuntimeClient({ baseURL: "https://runtime.test", fetch: fetcher });
    const iterator = feedFor(client, kind, { signal: abort.signal });
    expect((await iterator.next()).value?.seq).toBe(1);
    expect(listeners(abort.signal)).toBe(1);
    abort.abort();
    expect(listeners(abort.signal)).toBe(0);
    expect((await iterator.next()).done).toBe(true);
  });
});
