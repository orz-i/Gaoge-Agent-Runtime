import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { RuntimeClient, RuntimeAPIError } from "@orz-i/agent-runtime-client";

const fixture = (name) => JSON.parse(readFileSync(new URL(`./fixtures/${name}.json`, import.meta.url), "utf8"));
const snapshot = fixture("snapshot-completed");
const events = fixture("events");
const cancelled = fixture("cancel");
const feedEvent = fixture("feed-terminal");
const error = fixture("error");
const extension = fixture("snapshot-extension");
const calls = [];

// Exercise the published ESM entrypoint with the same JSON verified by Go and
// OpenAPI. No server, provider credentials or outbound requests are needed.
const client = new RuntimeClient({
  baseURL: "https://runtime.example/api/v1",
  headers: async () => ({ Authorization: "Bearer consumer-token" }),
  fetch: async (input, init) => {
    const url = new URL(input);
    const route = `${init.method ?? "GET"} ${url.pathname}${url.search}`;
    calls.push(route);
    assert.equal(new Headers(init.headers).get("Authorization"), "Bearer consumer-token");
    switch (route) {
      case "GET /api/v1/runs/run-1":
        return Response.json(snapshot);
      case "GET /api/v1/runs/extension":
        return Response.json(extension);
      case "GET /api/v1/runs/run-1/events?afterSeq=1&limit=10":
        return Response.json(events);
      case "POST /api/v1/runs/run-1/cancel":
        assert.deepEqual(JSON.parse(init.body), { expectedRevision: 1, reason: "stop" });
        return Response.json(cancelled);
      case "GET /api/v1/runs/run-1/feed?afterSeq=2":
        return new Response(`id: 3\ndata: ${JSON.stringify(feedEvent)}\n\n`, {
          headers: { "Content-Type": "text/event-stream" },
        });
      case "GET /api/v1/runs/conflict":
        return Response.json(error, { status: 409 });
      default:
        throw new Error(`Unexpected consumer request: ${route}`);
    }
  },
});

assert.deepEqual(await client.runs.get("run-1"), snapshot);
assert.deepEqual(await client.runs.get("extension"), extension);
assert.deepEqual(await client.runs.events("run-1", { afterSeq: 1, limit: 10 }), events);
assert.deepEqual(await client.runs.cancel("run-1", { expectedRevision: 1, reason: "stop" }), cancelled);
const received = [];
for await (const event of client.runs.feed("run-1", { afterSeq: 2, maxReconnects: 0 })) received.push(event);
assert.deepEqual(received, [feedEvent]);
await assert.rejects(client.runs.get("conflict"), (failure) => {
  assert.ok(failure instanceof RuntimeAPIError);
  assert.equal(failure.status, 409);
  assert.equal(failure.code, error.error.code);
  assert.equal(failure.message, error.error.message);
  assert.equal(failure.requestID, error.error.requestID);
  return true;
});
assert.equal(calls.length, 6);
