import type {
  ContinuationJobDTO, ContinuationJobFilterDTO, ContinuationJobPageDTO, EvidenceDTO, OutputCatalogPageDTO, OutputDetailDTO, OutputDTO, OutputPreviewDTO,
  OutputVersionPageDTO, PlanViewDTO, RunCheckpointDTO, RunEventDetailDTO, RunEventDTO, RunEventHistoryPage,
  RunInteractionDTO, RunQueueItemDTO, RunQueueRequestDTO, RuntimeEvidenceSelectionDTO,
  RuntimeEvidenceSourceDTO, StartTextRunRequest, StartTextRunResult, TextRunDetailDTO,
  TextRunDTO, WorkbenchDTO,
} from "./types.js";

export type RuntimeHeaders = HeadersInit | (() => HeadersInit | Promise<HeadersInit>);
export type RuntimeClientOptions = { baseURL: string; fetch?: typeof globalThis.fetch; headers?: RuntimeHeaders; maxStreamRetries?: number };
export type RequestOptions = { signal?: AbortSignal };

export class RuntimeAPIError extends Error {
  constructor(message: string, public readonly status: number, public readonly code: string, public readonly requestID: string) {
    super(message); this.name = "RuntimeAPIError";
  }
}

type ErrorResponse = { error?: { code?: string; message?: string; requestID?: string } };
type OutputWireDTO = Omit<OutputDTO, "extension"> & { artifact?: OutputDTO["extension"] };
type OutputCatalogWireDTO = OutputWireDTO & { sourceRun: OutputDetailDTO["sourceRun"]; thread: OutputDetailDTO["sourceThread"] };

const pathPart = (value: string): string => encodeURIComponent(value);
const streamClosingEvents = new Set(["run.completed", "run.failed", "run.cancelled", "run.waiting_input", "run.suspended"]);

function outputFromWire<T extends OutputWireDTO>(item: T): Omit<T, "artifact"> & Pick<OutputDTO, "extension"> {
  const { artifact, ...output } = item; return { ...output, extension: artifact };
}
function catalogOutputFromWire(item: OutputCatalogWireDTO): OutputDetailDTO {
  const { thread, ...output } = outputFromWire(item); return { ...output, sourceThread: thread };
}
function validProjectionRef(value: unknown): value is {kind:string;id:string} {
  if (!value || typeof value !== "object") return false;
  const ref=value as Record<string,unknown>;
  return typeof ref.kind==="string"&&ref.kind.length>0&&typeof ref.id==="string"&&ref.id.length>0;
}
function requireRunProjectionRefs<T extends StartTextRunResult|TextRunDetailDTO>(value:T):T {
  if (validProjectionRef(value.inputProjectionRef)&&validProjectionRef(value.outputProjectionRef)) return value;
  throw new RuntimeAPIError("runtime returned invalid projection references",502,"runtime.invalid_response",value.run?.requestID??"");
}

export class RuntimeClient {
  readonly runs;
  readonly events;
  readonly interactions;
  readonly outputs;
  readonly evidence;
  readonly queue;
  readonly workbench;
  readonly admin;
  private readonly fetcher: typeof globalThis.fetch;
  private readonly maxStreamRetries: number;

  constructor(private readonly options: RuntimeClientOptions) {
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.maxStreamRetries = options.maxStreamRetries ?? 5;
    this.runs = {
      list: (thread: {kind:string;id:string}, page=1, pageSize=20, request?:RequestOptions) => this.request<{total:number;results:TextRunDTO[]}>(`/runs?threadKind=${pathPart(thread.kind)}&threadID=${pathPart(thread.id)}&page=${page}&pageSize=${pageSize}`, {}, request),
      create: async(payload:StartTextRunRequest, request?:RequestOptions) => requireRunProjectionRefs(await this.request<StartTextRunResult>("/runs", {method:"POST", body:JSON.stringify(payload)}, request)),
      get: async(runID:string, request?:RequestOptions) => requireRunProjectionRefs(await this.request<TextRunDetailDTO>(`/runs/${pathPart(runID)}`, {}, request)),
      cancel: (runID:string, request?:RequestOptions) => this.request<{canceled:boolean}>(`/runs/${pathPart(runID)}/cancel`, {method:"POST"}, request),
      resume: (runID:string,payload:{checkpointID?:string;clientResumeID:string},request?:RequestOptions)=>this.request<{checkpointID:string;runID:string;status:string;reused:boolean}>(`/runs/${pathPart(runID)}/resume`,{method:"POST",body:JSON.stringify(payload)},request),
      retire: (runID:string,request?:RequestOptions)=>this.request<{runID:string;status:"cancelled";reused:boolean}>(`/runs/${pathPart(runID)}/retire`,{method:"POST"},request),
      plan: (runID:string,request?:RequestOptions)=>this.request<PlanViewDTO>(`/runs/${pathPart(runID)}/plan`,{},request),
      checkpoints: async (runID:string,request?:RequestOptions)=>(await this.request<{results:RunCheckpointDTO[]}>(`/runs/${pathPart(runID)}/checkpoints`,{},request)).results ?? [],
    };
    this.events = {
      get:(runID:string,eventID:string,request?:RequestOptions)=>this.request<RunEventDetailDTO>(`/runs/${pathPart(runID)}/events/${pathPart(eventID)}`,{},request),
      history:(runID:string,options:{beforeSeq?:number;limit?:number}&RequestOptions={})=>{const q=new URLSearchParams();if(options.beforeSeq)q.set("beforeSeq",String(options.beforeSeq));if(options.limit)q.set("limit",String(options.limit));return this.request<RunEventHistoryPage>(`/runs/${pathPart(runID)}/events/history${q.size?`?${q}`:""}`,{},options)},
      stream:(runID:string,afterSeq:number,onEvent:(event:RunEventDTO)=>void,request?:RequestOptions)=>this.stream(runID,afterSeq,onEvent,request?.signal),
    };
    this.interactions = {
      list:async(runID:string,request?:RequestOptions)=>(await this.request<{results:RunInteractionDTO[]}>(`/runs/${pathPart(runID)}/interactions`,{},request)).results??[],
      resolve:(runID:string,id:string,payload:{clientResolveID:string;response:Record<string,unknown>},request?:RequestOptions)=>this.request<RunInteractionDTO>(`/runs/${pathPart(runID)}/interactions/${pathPart(id)}/resolve`,{method:"POST",body:JSON.stringify(payload)},request),
    };
    this.outputs = {
      forRun:async(runID:string,request?:RequestOptions)=>(await this.request<{results:OutputWireDTO[]}>(`/runs/${pathPart(runID)}/outputs`,{},request)).results.map(outputFromWire),
      list:async(options:{q?:string;cursor?:string;limit?:number}&RequestOptions={}):Promise<OutputCatalogPageDTO>=>{const q=new URLSearchParams();if(options.q)q.set("q",options.q);if(options.cursor)q.set("cursor",options.cursor);if(options.limit)q.set("limit",String(options.limit));const page=await this.request<{results?:OutputCatalogWireDTO[];nextCursor?:string}>(`/outputs${q.size?`?${q}`:""}`,{},options);return{results:(page.results??[]).map(catalogOutputFromWire),nextCursor:page.nextCursor}},
      get:async(id:string,version?:number,request?:RequestOptions)=>catalogOutputFromWire(await this.request<OutputCatalogWireDTO>(`/outputs/${pathPart(id)}${version?`?version=${version}`:""}`,{},request)),
      versions:async(id:string,options:{cursor?:string;limit?:number}&RequestOptions={}):Promise<OutputVersionPageDTO>=>{const q=new URLSearchParams();if(options.cursor)q.set("cursor",options.cursor);if(options.limit)q.set("limit",String(options.limit));const page=await this.request<{results?:OutputCatalogWireDTO[];hasMore?:boolean;nextCursor?:string}>(`/outputs/${pathPart(id)}/versions${q.size?`?${q}`:""}`,{},options);return{results:(page.results??[]).map(catalogOutputFromWire),hasMore:page.hasMore??false,nextCursor:page.nextCursor}},
      preview:async(id:string,version:number,request?:RequestOptions)=>{const result=await this.request<{output:OutputCatalogWireDTO;preview:OutputPreviewDTO}>(`/outputs/${pathPart(id)}/versions/${version}/preview`,{},request);return{...result,output:catalogOutputFromWire(result.output)}},
      download:(id:string,version:number,request?:RequestOptions)=>this.raw(`/outputs/${pathPart(id)}/versions/${version}/download`,{},request),
    };
    this.evidence = { create:(source:RuntimeEvidenceSourceDTO,selection:RuntimeEvidenceSelectionDTO,request?:RequestOptions)=>this.request<EvidenceDTO>("/evidence",{method:"POST",body:JSON.stringify({source,selection})},request) };
    this.queue = {
      list:async(thread:{kind:string;id:string},request?:RequestOptions)=>(await this.request<{results:RunQueueItemDTO[]}>(`/run-queue?threadKind=${pathPart(thread.kind)}&threadID=${pathPart(thread.id)}`,{},request)).results??[],
      create:(payload:RunQueueRequestDTO&{clientQueueID:string},request?:RequestOptions)=>this.request<RunQueueItemDTO&{reused:boolean}>("/run-queue",{method:"POST",body:JSON.stringify(payload)},request),
      update:(id:string,payload:RunQueueRequestDTO&{expectedRevision:number},request?:RequestOptions)=>this.request<RunQueueItemDTO>(`/run-queue/${pathPart(id)}`,{method:"PATCH",body:JSON.stringify(payload)},request),
      cancel:(thread:{kind:string;id:string},id:string,request?:RequestOptions)=>this.request<RunQueueItemDTO>(`/run-queue/${pathPart(id)}?threadKind=${pathPart(thread.kind)}&threadID=${pathPart(thread.id)}`,{method:"DELETE"},request),
      prioritize:(thread:{kind:string;id:string},id:string,request?:RequestOptions)=>this.request<RunQueueItemDTO>(`/run-queue/${pathPart(id)}/prioritize?threadKind=${pathPart(thread.kind)}&threadID=${pathPart(thread.id)}`,{method:"POST"},request),
      interruptAndSend:(thread:{kind:string;id:string},id:string,request?:RequestOptions)=>this.request<RunQueueItemDTO>(`/run-queue/${pathPart(id)}/interrupt-and-send?threadKind=${pathPart(thread.kind)}&threadID=${pathPart(thread.id)}`,{method:"POST"},request),
    };
    this.workbench = { get:async(runID:string,request?:RequestOptions)=>{const view=await this.request<Omit<WorkbenchDTO,"outputs">&{outputs:OutputWireDTO[]}>(`/runs/${pathPart(runID)}/workbench`,{},request);return{...view,outputs:(view.outputs??[]).map(outputFromWire)}} };
    this.admin = {
      continuations: {
        list:(options:ContinuationJobFilterDTO&RequestOptions={})=>{const q=new URLSearchParams();for(const [key,value] of Object.entries(options)){if(key!=="signal"&&value!==undefined&&value!=="")q.set(key,String(value))}return this.request<ContinuationJobPageDTO>(`/admin/agentruntime/continuations${q.size?`?${q}`:""}`,{},options)},
        requeue:(jobID:string,payload:{reason:string},request?:RequestOptions)=>this.request<ContinuationJobDTO>(`/admin/agentruntime/continuations/${pathPart(jobID)}/requeue`,{method:"POST",body:JSON.stringify(payload)},request),
      },
    };
  }

  private async headers(): Promise<Headers> { const source=typeof this.options.headers==="function"?await this.options.headers():this.options.headers;const headers=new Headers(source);headers.set("Accept","application/json");return headers }
  private url(path:string):string{return `${this.options.baseURL.replace(/\/$/,"")}${path}`}
  private async raw(path:string,init:RequestInit={},request?:RequestOptions):Promise<Response>{const headers=await this.headers();if(init.body)headers.set("Content-Type","application/json");const response=await this.fetcher(this.url(path),{...init,headers,signal:request?.signal});if(response.ok)return response;let payload:ErrorResponse={};try{payload=await response.json() as ErrorResponse}catch{/* non-JSON failures use stable fallback */}throw new RuntimeAPIError(payload.error?.message??`runtime request failed: ${response.status}`,response.status,payload.error?.code??"runtime.internal",payload.error?.requestID??response.headers.get("x-request-id")??"")}
  private async request<T>(path:string,init:RequestInit={},request?:RequestOptions):Promise<T>{const response=await this.raw(path,init,request);if(response.status===204)return undefined as T;return response.json() as Promise<T>}
  private async stream(runID:string,afterSeq:number,onEvent:(event:RunEventDTO)=>void,signal?:AbortSignal):Promise<void>{let last=Math.max(0,Math.floor(afterSeq));for(let attempt=0;;attempt++){try{const response=await this.raw(`/runs/${pathPart(runID)}/events?afterSeq=${last}`,{headers:{Accept:"application/x-ndjson"}},{signal});if(!response.body)return;const reader=response.body.pipeThrough(new TextDecoderStream()).getReader();let buffer="";while(true){const{done,value}=await reader.read();buffer+=value??"";const lines=buffer.split("\n");buffer=lines.pop()??"";for(const line of lines){if(!line.trim())continue;const event=JSON.parse(line) as RunEventDTO;if(event.seq<=last)continue;last=event.seq;onEvent(event);if(streamClosingEvents.has(event.type)){await reader.cancel();return}}if(done)break}if(buffer.trim()){const event=JSON.parse(buffer) as RunEventDTO;if(event.seq>last){last=event.seq;onEvent(event);if(streamClosingEvents.has(event.type))return}}throw new TypeError("runtime event stream disconnected")}catch(error){if(signal?.aborted)throw signal.reason??error;if(error instanceof RuntimeAPIError||attempt>=this.maxStreamRetries)throw error;await new Promise<void>((resolve,reject)=>{const timer=setTimeout(resolve,Math.min(250*2**attempt,4000));signal?.addEventListener("abort",()=>{clearTimeout(timer);reject(signal.reason??new DOMException("Aborted","AbortError"))},{once:true})})}}}
}
