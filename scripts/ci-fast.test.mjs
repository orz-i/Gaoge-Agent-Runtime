import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, writeFileSync, chmodSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { additionalFastModules, missingRequiredPasses } from "./ci-fast.mjs";
import { fullImpact } from "./ci-impact-policy.mjs";

test("Fast only adds scoped/direct optional modules; Full fallback cannot suppress the fixed native floor", () => {
  assert.deepEqual(additionalFastModules({recommendation:"go",affectedGoModules:["go/agent-runtime","go/agent-runtime-provider","go/agent-runtime-harness-postgres"]}),["go/agent-runtime-provider"]);
  assert.deepEqual(additionalFastModules(fullImpact("risk",[{path:"go/agent-runtime/agent/agent.go"},{path:"go/agent-runtime-acp/adapter.go"}])),["go/agent-runtime-acp"]);
  assert.deepEqual(additionalFastModules(fullImpact("unavailable")),[]);
});
test("real-engine anchors need actual pass events, not empty selection, skips or foreign package names", () => {
  const expected=[["go/agent-runtime-postgres","TestRequired/callback-entry"]];
  const Package="github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres";
  const Test=expected[0][1];
  for (const events of [[],[{Action:"skip",Package,Test}],[{Action:"pass",Package,Test:"TestRequired"}],[{Action:"pass",Package:"other",Test}],[{Action:"pass",Package}]]) {
    assert.deepEqual(missingRequiredPasses(events,expected),expected);
  }
  assert.deepEqual(missingRequiredPasses([{Action:"pass",Package,Test}],expected),[]);
});

test("Full integration shards partition the exact original suite commands; default still runs all", (t) => {
  const directory = mkdtempSync(path.join(tmpdir(), "ci-integration-plan-"));
  t.after(() => rmSync(directory,{recursive:true,force:true}));
  const go = path.join(directory,"go");
  writeFileSync(go, '#!/usr/bin/env node\nconsole.log(JSON.stringify({cwd:process.cwd(),args:process.argv.slice(2),work:process.env.GOWORK}));\n');
  chmodSync(go,0o700);
  const script=fileURLToPath(new URL("./run-integration.mjs",import.meta.url));
  const env={...process.env,PATH:directory+path.delimiter+process.env.PATH,TEST_POSTGRES_DSN:"fixture",TEST_REDIS_ADDR:"fixture",GOWORK:"auto"};
  const run=(...args)=>spawnSync(process.execPath,[script,"--external-services",...args],{env,encoding:"utf8"});
  const parse=(result)=>{assert.equal(result.status,0,result.stderr);return result.stdout.trim().split("\n").map(JSON.parse);};
  const full=parse(run());
  assert.equal(full.length,4);
  const shards=["kernel","harness","redis","continuation"].flatMap(suite=>parse(run("--suite="+suite)));
  assert.deepEqual(shards,full);
  assert.deepEqual(full.map(row=>row.args),["^TestRealPostgres","^TestRealPostgres","^TestRealRedis","^(TestRealPostgres|TestPR151)"].map(pattern=>["test","-race","./...","-run",pattern,"-count=1","-v"]));
  assert.equal(full.at(-1).work,"off");
  for(const args of [["--suite=unknown"],["--suite=kernel","--suite=harness"],["--unknown"]]) assert.notEqual(run(...args).status,0);
});
