import type {
  CancelRunRequest,
  CancelRunResponse,
  ResolvePlanApprovalRequest,
  ResolveWorkflowWaitRequest,
  RunSnapshotDTO,
  StartPlanRunRequest,
  StartTeamRunRequest,
  StartTextRunRequest,
  StartWorkflowRunRequest,
  WorkbenchDTO,
} from "./types.js";

export type RuntimeHeaders = HeadersInit | (() => HeadersInit | Promise<HeadersInit>);
export type RuntimeClientOptions = { baseURL: string; fetch?: typeof globalThis.fetch; headers?: RuntimeHeaders };
export type RequestOptions = { signal?: AbortSignal };

export class RuntimeAPIError extends Error {
  constructor(message: string, public readonly status: number, public readonly code: string, public readonly requestID: string) {
    super(message);
    this.name = "RuntimeAPIError";
  }
}

type ErrorResponse = { error?: { code?: string; message?: string; requestID?: string } };
const pathPart = (value: string): string => encodeURIComponent(value);

export class RuntimeClient {
  readonly text;
  readonly plans;
  readonly workflows;
  readonly teams;
  readonly runs;
  private readonly fetcher: typeof globalThis.fetch;

  constructor(private readonly options: RuntimeClientOptions) {
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.text = {
      start: (payload: StartTextRunRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>("/text-runs", { method: "POST", body: JSON.stringify(payload) }, request),
    };
    this.plans = {
      start: (payload: StartPlanRunRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>("/plan-runs", { method: "POST", body: JSON.stringify(payload) }, request),
      approve: (runID: string, payload: ResolvePlanApprovalRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>(`/plan-runs/${pathPart(runID)}/approval`, { method: "POST", body: JSON.stringify(payload) }, request),
    };
    this.workflows = {
      start: (payload: StartWorkflowRunRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>("/workflow-runs", { method: "POST", body: JSON.stringify(payload) }, request),
      resolveWait: (runID: string, payload: ResolveWorkflowWaitRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>(`/workflow-runs/${pathPart(runID)}/wait`, { method: "POST", body: JSON.stringify(payload) }, request),
    };
    this.teams = {
      start: (payload: StartTeamRunRequest, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>("/team-runs", { method: "POST", body: JSON.stringify(payload) }, request),
    };
    this.runs = {
      get: (runID: string, request?: RequestOptions) =>
        this.request<RunSnapshotDTO>(`/runs/${pathPart(runID)}`, {}, request),
      cancel: (runID: string, payload: CancelRunRequest, request?: RequestOptions) =>
        this.request<CancelRunResponse>(`/runs/${pathPart(runID)}/cancel`, { method: "POST", body: JSON.stringify(payload) }, request),
      workbench: (runID: string, request?: RequestOptions) =>
        this.request<WorkbenchDTO>(`/runs/${pathPart(runID)}/workbench`, {}, request),
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
      let payload: ErrorResponse = {};
      try {
        payload = await response.json() as ErrorResponse;
      } catch {
        // Non-JSON failures use stable fallback values.
      }
      throw new RuntimeAPIError(
        payload.error?.message ?? `runtime request failed: ${response.status}`,
        response.status,
        payload.error?.code ?? "runtime.internal",
        payload.error?.requestID ?? response.headers.get("x-request-id") ?? "",
      );
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }
}
