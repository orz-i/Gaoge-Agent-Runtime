import { describe, expect, it, vi } from "vitest";
import { RuntimeClient } from "./runtime-client.js";

const json = (data:unknown) => new Response(JSON.stringify(data),{status:200,headers:{"content-type":"application/json"}});
const output={outputID:"output-1",runID:"run-1",stepID:"step-1",sourceEventID:"event-1",sourceSnapshotID:"snapshot-1",parentOutputID:"output-0",status:"published",kind:"artifact",title:"Draft",summary:"Ready",artifact:{artifactType:"change_set",artifactID:"change-1"},projectionRef:{kind:"host.projection",id:"projection-1"},version:2,createdAt:"2026-07-22T00:00:00Z"};

describe("RuntimeClient",()=>{
  it("owns wire normalization",async()=>{const fetcher=vi.fn().mockResolvedValueOnce(json({results:[output]})).mockResolvedValueOnce(json({outputs:[output]}));const client=new RuntimeClient({baseURL:"https://runtime.test/v1",fetch:fetcher});const outputs=await client.outputs.forRun("run-1");const workbench=await client.workbench.get("run-1");expect(outputs[0]).toMatchObject({extension:{artifactType:"change_set"}});expect(outputs[0]).not.toHaveProperty("artifact");expect(workbench.outputs[0]?.extension).toMatchObject({artifactID:"change-1"})});
  it("sends neutral evidence references",async()=>{const fetcher=vi.fn().mockResolvedValue(json({evidenceID:"evidence-1"}));const client=new RuntimeClient({baseURL:"https://runtime.test/v1",fetch:fetcher});await client.evidence.create({kind:"projection",thread:{kind:"host.thread",id:"thread-1"},projection:{kind:"host.projection",id:"projection-1"}},{kind:"full"});expect(JSON.parse(String(fetcher.mock.calls[0]?.[1]?.body))).toEqual({source:{kind:"projection",thread:{kind:"host.thread",id:"thread-1"},projection:{kind:"host.projection",id:"projection-1"}},selection:{kind:"full"}})});
  it("returns stable API errors",async()=>{const failing=vi.fn().mockResolvedValue(new Response(JSON.stringify({error:{code:"run.conflict",message:"conflict",requestID:"request-1"}}),{status:409}));const client=new RuntimeClient({baseURL:"https://runtime.test/v1",fetch:failing});await expect(client.runs.get("run-1")).rejects.toMatchObject({status:409,code:"run.conflict",requestID:"request-1"})});
});
