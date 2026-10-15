import type {
  CancelRunRequest,
  CancelRunResponse,
  HarnessTurnFeedEventDTO,
  HarnessTurnSnapshotDTO,
  HarnessCommandDTO,
  RunEventPageDTO,
  RunSnapshotDTO,
  RunFeedEventDTO,
  WorkbenchDTO,
} from "./types.js";
import { createAgentCapability } from "./capabilities/agent.js";
import { pathPart } from "./capabilities/shared.js";
import { createTeamsCapability } from "./capabilities/teams.js";
import { createWorkflowsCapability } from "./capabilities/workflows.js";

export type RuntimeHeaders = HeadersInit | (() => HeadersInit | Promise<HeadersInit>);
export type RuntimeClientOptions = { baseURL: string; fetch?: typeof globalThis.fetch; headers?: RuntimeHeaders };
export type RequestOptions = { signal?: AbortSignal };
export type RunEventsOptions = RequestOptions & { afterSeq?: number; limit?: number };
type FeedOptions = RequestOptions & {
  afterSeq?: number;
  reconnectDelayMS?: number;
  maxReconnects?: number;
};
export type RunFeedOptions = FeedOptions & {
  onCursorExpired?: (snapshot: RunSnapshotDTO) => void | Promise<void>;
};
export type HarnessTurnFeedOptions = FeedOptions & {
  onCursorExpired?: (snapshot: HarnessTurnSnapshotDTO) => void | Promise<void>;
};

type FeedProtocol = {
  feedPath: string;
  snapshotPath: string;
  cursorExpiredCode: string;
  cursorHeadHeader: string;
  invalidResponseCode: string;
  disconnectedCode: string;
  feedName: string;
};

class FeedReadError extends Error {
  constructor(readonly source: unknown) {
    super("SSE stream read failed");
  }
}

export class RuntimeAPIError extends Error {
  constructor(message: string, public readonly status: number, public readonly code: string, public readonly requestID: string) {
    super(message);
    this.name = "RuntimeAPIError";
  }
}

// AsyncGenerator.return() normally waits for any pending next() to finish.
// Expose a separate close path so callers can interrupt a suspended SSE read
// (and fetch) before the generator gets a chance to run its own finally block.
function interruptibleFeed<T>(
  factory: (signal: AbortSignal) => AsyncGenerator<T>,
  externalSignal?: AbortSignal,
): AsyncGenerator<T> {
  const controller = new AbortController();
  const source = factory(controller.signal);
  let closed = false;
  let started = false;
  const stopped = new Promise<IteratorResult<T>>((resolve) => {
    controller.signal.addEventListener("abort", () => resolve({ done: true, value: undefined }), { once: true });
  });
  const stop = () => {
    if (closed) return;
    closed = true;
    externalSignal?.removeEventListener("abort", onExternalAbort);
    controller.abort();
  };
  const shutdown = (value?: unknown) => {
    if (closed) return;
    stop();
    // The underlying generator may still be inside next(). Do not wait for a
    // non-cooperative fetch or ReadableStream cancellation to finish.
    void source.return(value).catch(() => {});
  };
  function onExternalAbort() { shutdown(); }

  return {
    async next(...args: [] | [unknown]) {
      if (closed) return { done: true, value: undefined };
      if (!started) {
        started = true;
        // Merely creating a feed must not retain a long-lived caller signal.
        // Subscribe only when the first next() actually starts consuming it.
        externalSignal?.addEventListener("abort", onExternalAbort, { once: true });
        // Covers a signal aborted before next(), including the subscription edge.
        if (externalSignal?.aborted) shutdown();
      }
      if (closed) return { done: true, value: undefined };
      try {
        const result = await Promise.race([source.next(...args), stopped]);
        if (closed) return { done: true, value: undefined };
        if (result.done) stop();
        return result;
      } catch (error) {
        if (closed) return { done: true, value: undefined };
        stop();
        throw error;
      }
    },
    return(value) {
      shutdown(value);
      return Promise.resolve({ done: true, value });
    },
    throw(error) {
      shutdown();
      return Promise.reject(error);
    },
    [Symbol.asyncIterator]() { return this; },
    [Symbol.asyncDispose]() {
      shutdown();
      return Promise.resolve();
    },
  };
}

async function* decodeHarnessTurnFeed(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<HarnessTurnFeedEventDTO> {
  for await (const data of decodeSSEData(body, signal)) {
    const event = JSON.parse(data) as HarnessTurnFeedEventDTO;
    if (
      Number.isSafeInteger(event.seq) &&
      event.seq > 0 &&
      typeof event.turnID === "string" &&
      typeof event.type === "string"
    ) {
      yield event;
    }
  }
}

async function* decodeSSEData(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<string> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let reachedEOF = false;
  let cancelRequested = false;
  const cancelReader = () => {
    if (reachedEOF || cancelRequested) return;
    cancelRequested = true;
    try {
      // reader.cancel() resolves pending read()s, but its own promise depends on
      // the stream's cancel hook, which may never settle. Observe errors without
      // awaiting that hook during shutdown.
      void reader.cancel().catch(() => {});
    } catch {
      // The stream can already be errored or have released its reader.
    }
  };
  signal?.addEventListener("abort", cancelReader, { once: true });
  if (signal?.aborted) cancelReader();
  try {
    while (!signal?.aborted) {
      let chunk: ReadableStreamReadResult<Uint8Array>;
      try {
        chunk = await reader.read();
      } catch (error) {
        if (signal?.aborted) return;
        // Only transport read failures are retryable, not JSON parsing errors.
        throw new FeedReadError(error);
      }
      if (signal?.aborted) return;
      const { done, value } = chunk;
      buffer += decoder.decode(value, { stream: !done });
      buffer = buffer.replaceAll("\r\n", "\n");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        if (signal?.aborted) return;
        const data = sseData(buffer.slice(0, boundary));
        buffer = buffer.slice(boundary + 2);
        if (data) yield data;
        boundary = buffer.indexOf("\n\n");
      }
      if (done) {
        reachedEOF = true;
        if (signal?.aborted) return;
        const data = sseData(buffer);
        if (data) yield data;
        return;
      }
    }
  } finally {
    signal?.removeEventListener("abort", cancelReader);
    cancelReader();
    reader.releaseLock();
  }
}

function sseData(block: string): string {
  return block
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice(5).trimStart())
    .join("\n")
    .trim();
}

type ErrorResponse = { error?: { code?: string; message?: string; requestID?: string } };

export class RuntimeClient {
  readonly agent: ReturnType<typeof createAgentCapability>;
  readonly workflows: ReturnType<typeof createWorkflowsCapability>;
  readonly teams: ReturnType<typeof createTeamsCapability>;
  readonly harness;
  readonly runs;
  private readonly fetcher: typeof globalThis.fetch;

  constructor(private readonly options: RuntimeClientOptions) {
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    const capabilityRequest = <T>(path: string, init: RequestInit = {}, request?: RequestOptions) =>
      this.request<T>(path, init, request);
    this.agent = createAgentCapability(capabilityRequest);
    this.workflows = createWorkflowsCapability(capabilityRequest);
    this.teams = createTeamsCapability(capabilityRequest);
    this.harness = {
      commands: {
        list: (request?: RequestOptions) => this.request<HarnessCommandDTO[]>("/harness/commands", {}, request),
      },
      turns: {
        get: (turnID: string, request?: RequestOptions) =>
          this.request<HarnessTurnSnapshotDTO>(`/harness/turns/${pathPart(turnID)}`, {}, request),
        feed: (turnID: string, request?: HarnessTurnFeedOptions) => this.streamHarnessTurnFeed(turnID, request),
        grantCallAllowance: (
          turnID: string,
          expectedTurnRevision: number,
          increment: { modelCalls?: number; toolCalls?: number },
          request?: RequestOptions,
        ) => this.request<HarnessTurnSnapshotDTO>(
          `/harness/turns/${pathPart(turnID)}/allowance`,
          { method: "POST", body: JSON.stringify({ expectedTurnRevision, ...increment }) },
          request,
        ),
        resolveApproval: (
          turnID: string,
          decision: "approve" | "reject",
          comment = "",
          request?: RequestOptions,
        ) => this.request<HarnessTurnSnapshotDTO>(
          `/harness/turns/${pathPart(turnID)}/approval`,
          { method: "POST", body: JSON.stringify({ decision, comment }) },
          request,
        ),
        resolveInteraction: (
          turnID: string,
          interactionID: string,
          response: unknown,
          request?: RequestOptions,
        ) => this.request<HarnessTurnSnapshotDTO>(
          `/harness/turns/${pathPart(turnID)}/interactions/${pathPart(interactionID)}`,
          { method: "POST", body: JSON.stringify({ response }) },
          request,
        ),
        retryInvocation: (turnID: string, invocationID: string, request?: RequestOptions) =>
          this.request<HarnessTurnSnapshotDTO>(
            `/harness/turns/${pathPart(turnID)}/invocations/${pathPart(invocationID)}/retry`,
            { method: "POST" },
            request,
          ),
        createSubtask: (turnID: string, roleID: string, goal: string, request?: RequestOptions) =>
          this.request<HarnessTurnSnapshotDTO>(
            `/harness/turns/${pathPart(turnID)}/subtasks`,
            { method: "POST", body: JSON.stringify({ roleID, goal }) }, request,
          ),
        cancelSubtask: (turnID: string, subtaskID: string, reason = "", request?: RequestOptions) =>
          this.request<HarnessTurnSnapshotDTO>(
            `/harness/turns/${pathPart(turnID)}/subtasks/${pathPart(subtaskID)}/cancel`,
            { method: "POST", body: JSON.stringify({ reason }) }, request,
          ),
        resolveSubtaskApproval: (
          turnID: string, subtaskID: string, checkpointID: string,
          decision: "approve" | "reject", comment = "", request?: RequestOptions,
        ) => this.request<HarnessTurnSnapshotDTO>(
          `/harness/turns/${pathPart(turnID)}/subtasks/${pathPart(subtaskID)}/approval`,
          { method: "POST", body: JSON.stringify({ checkpointID, decision, comment }) }, request,
        ),
      },
    };
    this.runs = {
      get: (runID: string, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>(`/runs/${pathPart(runID)}`, {}, request),
      events: (runID: string, options: RunEventsOptions = {}) => {
        const parameters = new URLSearchParams();
        if (options.afterSeq !== undefined) parameters.set("afterSeq", String(options.afterSeq));
        if (options.limit !== undefined) parameters.set("limit", String(options.limit));
        const query = parameters.size > 0 ? `?${parameters.toString()}` : "";
        return this.request<RunEventPageDTO>(`/runs/${pathPart(runID)}/events${query}`, {}, options);
      },
      cancel: (runID: string, payload: CancelRunRequest, request?: RequestOptions) =>
        this.request<CancelRunResponse>(`/runs/${pathPart(runID)}/cancel`, { method: "POST", body: JSON.stringify(payload) }, request),
      workbench: (runID: string, request?: RequestOptions) =>
        this.request<WorkbenchDTO>(`/runs/${pathPart(runID)}/workbench`, {}, request),
      feed: (runID: string, request?: RunFeedOptions) => this.streamRunFeed(runID, request),
    };
  }

  private async headers(): Promise<Headers> {
    const source = typeof this.options.headers === "function" ? await this.options.headers() : this.options.headers;
    const headers = new Headers(source);
    headers.set("Accept", "application/json");
    return headers;
  }

  private url(path: string): string {
    return `${this.options.baseURL.replace(/\/$/, "")}${path}`;
  }

  private async request<T>(path: string, init: RequestInit = {}, request?: RequestOptions): Promise<T> {
    const headers = await this.headers();
    if (init.body) headers.set("Content-Type", "application/json");
    const response = await this.fetcher(this.url(path), { ...init, headers, signal: request?.signal });
    if (!response.ok) {
      throw await runtimeAPIError(response);
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }

  private streamHarnessTurnFeed(
    turnID: string,
    request: HarnessTurnFeedOptions = {},
  ): AsyncGenerator<HarnessTurnFeedEventDTO> {
    const path = "/harness/turns/" + pathPart(turnID);
    return interruptibleFeed((signal) => this.streamFeed<HarnessTurnFeedEventDTO, HarnessTurnSnapshotDTO>({
      feedPath: path + "/feed",
      snapshotPath: path,
      cursorExpiredCode: "harness.feed_cursor_expired",
      cursorHeadHeader: "x-harness-feed-head",
      invalidResponseCode: "harness.feed_invalid_response",
      disconnectedCode: "harness.feed_disconnected",
      feedName: "Harness Turn",
    }, decodeHarnessTurnFeed, { ...request, signal }), request.signal);
  }

  private streamRunFeed(runID: string, request: RunFeedOptions = {}): AsyncGenerator<RunFeedEventDTO> {
    const path = "/runs/" + pathPart(runID);
    return interruptibleFeed((signal) => this.streamFeed<RunFeedEventDTO, RunSnapshotDTO>({
      feedPath: path + "/feed",
      snapshotPath: path,
      cursorExpiredCode: "runfeed.cursor_expired",
      cursorHeadHeader: "x-run-feed-head",
      invalidResponseCode: "runfeed.invalid_response",
      disconnectedCode: "runfeed.disconnected",
      feedName: "runtime",
    }, decodeRunFeed, { ...request, signal }), request.signal);
  }

  private async *streamFeed<TEvent extends { seq: number; terminal?: boolean }, TSnapshot>(
    protocol: FeedProtocol,
    decode: (body: ReadableStream<Uint8Array>, signal?: AbortSignal) => AsyncGenerator<TEvent>,
    request: FeedOptions & { onCursorExpired?: (snapshot: TSnapshot) => void | Promise<void> },
  ): AsyncGenerator<TEvent> {
    let afterSeq = Math.max(0, Math.trunc(request.afterSeq ?? 0));
    const reconnectDelayMS = Math.max(0, Math.trunc(request.reconnectDelayMS ?? 250));
    const maxReconnects = Math.max(0, Math.trunc(request.maxReconnects ?? 20));
    let reconnects = 0;

    while (!request.signal?.aborted) {
      // Each connection owns its own abort controller, including when the
      // consumer closes an iterator suspended at a yield.
      const controller = new AbortController();
      const onAbort = () => controller.abort(request.signal?.reason);
      request.signal?.addEventListener("abort", onAbort, { once: true });
      if (request.signal?.aborted) onAbort();
      let retryDelay = false;
      try {
        let response: Response | undefined;
        try {
          const headers = await this.headers();
          headers.set("Accept", "text/event-stream");
          if (controller.signal.aborted) return;
          response = await this.fetcher(
            this.url(protocol.feedPath + "?afterSeq=" + afterSeq),
            { headers, signal: controller.signal },
          );
        } catch (error) {
          if (request.signal?.aborted) return;
          if (reconnects >= maxReconnects) throw error;
          reconnects += 1;
          retryDelay = true;
        }

        if (response) {
          if (!response.ok) {
            if (response.status === 404 && reconnects < maxReconnects) {
              reconnects += 1;
              retryDelay = true;
            } else {
              const apiError = await runtimeAPIError(response);
              if (apiError.code !== protocol.cursorExpiredCode) throw apiError;
              const headSeq = Number.parseInt(response.headers.get(protocol.cursorHeadHeader) ?? "", 10);
              if (!request.onCursorExpired || !Number.isSafeInteger(headSeq) || headSeq <= afterSeq) throw apiError;
              const snapshot = await this.request<TSnapshot>(
                protocol.snapshotPath, {}, { signal: request.signal },
              );
              await request.onCursorExpired(snapshot);
              afterSeq = headSeq;
              reconnects = 0;
            }
          } else if (!response.body) {
            throw new RuntimeAPIError(
              protocol.feedName + " feed response has no body", response.status, protocol.invalidResponseCode, "",
            );
          } else {
            try {
              for await (const event of decode(response.body, request.signal)) {
                if (request.signal?.aborted) return;
                if (event.seq <= afterSeq) continue;
                reconnects = 0;
                afterSeq = event.seq;
                yield event;
                if (event.terminal) return;
              }
            } catch (error) {
              if (request.signal?.aborted) return;
              if (!(error instanceof FeedReadError)) throw error;
              if (reconnects >= maxReconnects) throw error.source;
              reconnects += 1;
              retryDelay = true;
            }
            if (!retryDelay) {
              if (request.signal?.aborted) return;
              if (reconnects >= maxReconnects) {
                throw new RuntimeAPIError(
                  protocol.feedName + " feed disconnected", 0, protocol.disconnectedCode, "",
                );
              }
              // EOF without a terminal event is a reconnect, even if the
              // connection delivered data. Only new sequences reset the budget.
              reconnects += 1;
              retryDelay = true;
            }
          }
        }
      } finally {
        // Abort before any backoff and remove the external listener. Returning
        // early or throwing from a consumer closes this connection as well.
        controller.abort();
        request.signal?.removeEventListener("abort", onAbort);
      }
      if (retryDelay) await reconnectDelay(reconnectDelayMS, request.signal);
    }
  }
}

async function runtimeAPIError(response: Response): Promise<RuntimeAPIError> {
  let payload: ErrorResponse = {};
  try {
    payload = await response.json() as ErrorResponse;
  } catch {
    // Non-JSON failures use stable fallback values.
  }
  return new RuntimeAPIError(
    payload.error?.message ?? `runtime request failed: ${response.status}`,
    response.status,
    payload.error?.code ?? "runtime.internal",
    payload.error?.requestID ?? response.headers.get("x-request-id") ?? "",
  );
}

async function* decodeRunFeed(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<RunFeedEventDTO> {
  for await (const data of decodeSSEData(body, signal)) {
    const event = parseRunFeedData(data);
    if (event) yield event;
  }
}

function parseRunFeedData(data: string): RunFeedEventDTO | null {
  const event = JSON.parse(data) as RunFeedEventDTO;
  return Number.isSafeInteger(event.seq) && event.seq > 0 && typeof event.runID === "string" && typeof event.type === "string"
    ? event
    : null;
}

function reconnectDelay(durationMS: number, signal?: AbortSignal): Promise<void> {
  if (durationMS <= 0 || signal?.aborted) return Promise.resolve();
  return new Promise((resolve) => {
    const finish = () => {
      globalThis.clearTimeout(timeout);
      signal?.removeEventListener("abort", finish);
      resolve();
    };
    const timeout = globalThis.setTimeout(finish, durationMS);
    signal?.addEventListener("abort", finish, { once: true });
  });
}
