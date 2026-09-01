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
import { createPlansCapability } from "./capabilities/plans.js";
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

export class RuntimeAPIError extends Error {
  constructor(message: string, public readonly status: number, public readonly code: string, public readonly requestID: string) {
    super(message);
    this.name = "RuntimeAPIError";
  }
}

async function* decodeHarnessTurnFeed(body: ReadableStream<Uint8Array>): AsyncGenerator<HarnessTurnFeedEventDTO> {
  for await (const data of decodeSSEData(body)) {
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

async function* decodeSSEData(body: ReadableStream<Uint8Array>): AsyncGenerator<string> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      buffer = buffer.replaceAll("\r\n", "\n");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const data = sseData(buffer.slice(0, boundary));
        buffer = buffer.slice(boundary + 2);
        if (data) yield data;
        boundary = buffer.indexOf("\n\n");
      }
      if (done) {
        const data = sseData(buffer);
        if (data) yield data;
        return;
      }
    }
  } finally {
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
  readonly plans: ReturnType<typeof createPlansCapability>;
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
    this.plans = createPlansCapability(capabilityRequest);
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

  private async *streamHarnessTurnFeed(
    turnID: string,
    request: HarnessTurnFeedOptions = {},
  ): AsyncGenerator<HarnessTurnFeedEventDTO> {
    let afterSeq = Math.max(0, Math.trunc(request.afterSeq ?? 0));
    const reconnectDelayMS = Math.max(0, Math.trunc(request.reconnectDelayMS ?? 250));
    const maxReconnects = Math.max(0, Math.trunc(request.maxReconnects ?? 20));
    let reconnects = 0;
    while (!request.signal?.aborted) {
      let response: Response;
      try {
        const headers = await this.headers();
        headers.set("Accept", "text/event-stream");
        response = await this.fetcher(
          this.url(`/harness/turns/${pathPart(turnID)}/feed?afterSeq=${afterSeq}`),
          { headers, signal: request.signal },
        );
      } catch (error) {
        if (request.signal?.aborted) return;
        if (reconnects >= maxReconnects) throw error;
        reconnects += 1;
        await reconnectDelay(reconnectDelayMS, request.signal);
        continue;
      }
      if (!response.ok) {
        if (response.status === 404 && reconnects < maxReconnects) {
          reconnects += 1;
          await reconnectDelay(reconnectDelayMS, request.signal);
          continue;
        }
        const apiError = await runtimeAPIError(response);
        if (apiError.code === "harness.feed_cursor_expired") {
          const headSeq = Number.parseInt(response.headers.get("x-harness-feed-head") ?? "", 10);
          if (!request.onCursorExpired || !Number.isSafeInteger(headSeq) || headSeq <= afterSeq) throw apiError;
          const snapshot = await this.request<HarnessTurnSnapshotDTO>(
            `/harness/turns/${pathPart(turnID)}`,
            {},
            { signal: request.signal },
          );
          await request.onCursorExpired(snapshot);
          afterSeq = headSeq;
          reconnects = 0;
          continue;
        }
        throw apiError;
      }
      if (!response.body) {
        throw new RuntimeAPIError("Harness Turn feed response has no body", response.status, "harness.feed_invalid_response", "");
      }
      let received = false;
      for await (const event of decodeHarnessTurnFeed(response.body)) {
        if (event.seq <= afterSeq) continue;
        received = true;
        reconnects = 0;
        afterSeq = event.seq;
        yield event;
        if (event.terminal) return;
      }
      if (request.signal?.aborted) return;
      if (!received) reconnects += 1;
      if (reconnects > maxReconnects) {
        throw new RuntimeAPIError("Harness Turn feed disconnected", 0, "harness.feed_disconnected", "");
      }
      await reconnectDelay(reconnectDelayMS, request.signal);
    }
  }

  private async *streamRunFeed(runID: string, request: RunFeedOptions = {}): AsyncGenerator<RunFeedEventDTO> {
    let afterSeq = Math.max(0, Math.trunc(request.afterSeq ?? 0));
    const reconnectDelayMS = Math.max(0, Math.trunc(request.reconnectDelayMS ?? 250));
    const maxReconnects = Math.max(0, Math.trunc(request.maxReconnects ?? 20));
    let reconnects = 0;
    while (!request.signal?.aborted) {
      let response: Response;
      try {
        const headers = await this.headers();
        headers.set("Accept", "text/event-stream");
        response = await this.fetcher(
          this.url(`/runs/${pathPart(runID)}/feed?afterSeq=${afterSeq}`),
          { headers, signal: request.signal },
        );
      } catch (error) {
        if (request.signal?.aborted) return;
        if (reconnects >= maxReconnects) throw error;
        reconnects += 1;
        await reconnectDelay(reconnectDelayMS, request.signal);
        continue;
      }
      if (!response.ok) {
        if (response.status === 404 && reconnects < maxReconnects) {
          reconnects += 1;
          await reconnectDelay(reconnectDelayMS, request.signal);
          continue;
        }
        const apiError = await runtimeAPIError(response);
        if (apiError.code === "runfeed.cursor_expired") {
          const headSeq = Number.parseInt(response.headers.get("x-run-feed-head") ?? "", 10);
          if (!request.onCursorExpired || !Number.isSafeInteger(headSeq) || headSeq <= afterSeq) throw apiError;
          const snapshot = await this.request<RunSnapshotDTO>(
            `/runs/${pathPart(runID)}`,
            {},
            { signal: request.signal },
          );
          await request.onCursorExpired(snapshot);
          afterSeq = headSeq;
          reconnects = 0;
          continue;
        }
        throw apiError;
      }
      if (!response.body) {
        throw new RuntimeAPIError("runtime feed response has no body", response.status, "runfeed.invalid_response", "");
      }
      let received = false;
      for await (const event of decodeRunFeed(response.body)) {
        if (event.seq <= afterSeq) continue;
        received = true;
        reconnects = 0;
        afterSeq = event.seq;
        yield event;
        if (event.terminal) return;
      }
      if (request.signal?.aborted) return;
      if (!received) reconnects += 1;
      if (reconnects > maxReconnects) {
        throw new RuntimeAPIError("runtime feed disconnected", 0, "runfeed.disconnected", "");
      }
      await reconnectDelay(reconnectDelayMS, request.signal);
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

async function* decodeRunFeed(body: ReadableStream<Uint8Array>): AsyncGenerator<RunFeedEventDTO> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      buffer = buffer.replaceAll("\r\n", "\n");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const event = parseRunFeedBlock(block);
        if (event) yield event;
        boundary = buffer.indexOf("\n\n");
      }
      if (done) {
        const event = parseRunFeedBlock(buffer);
        if (event) yield event;
        return;
      }
    }
  } finally {
    reader.releaseLock();
  }
}

function parseRunFeedBlock(block: string): RunFeedEventDTO | null {
  const data = block
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice(5).trimStart())
    .join("\n")
    .trim();
  if (!data) return null;
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
