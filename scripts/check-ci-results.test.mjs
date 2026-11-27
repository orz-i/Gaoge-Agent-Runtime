import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { ciResultViolations, requiredJobs, fastJobs } from "./check-ci-results.mjs";

const results = (event, jobs = requiredJobs) => Object.fromEntries(jobs.map((job) => [job, {
  result: job === "observability" && event !== "push" ? "skipped" : "success",
}]));

test("the Full gate requires every original job and integration shard, except the established non-main Collector skip", () => {
  for (const [event,draft,gate] of [["pull_request",false,"full"],["push",null,"full"],["workflow_dispatch",null,"milestone"]]) {
    assert.deepEqual(ciResultViolations(results(event),event,draft,gate), []);
    for (const job of requiredJobs) {
      for (const result of ["failure", "cancelled", "skipped", "pending", undefined]) {
        if (job === "observability" && event !== "push" && result === "skipped") continue;
        const needs = results(event);
        needs[job] = { result };
        assert.notDeepEqual(ciResultViolations(needs,event,draft,gate), [], `${event}/${job}/${result}`);
      }
      const needs = results(event);
      delete needs[job];
      assert.notDeepEqual(ciResultViolations(needs,event,draft,gate), []);
    }
  }
});

test("Draft and manual milestone cannot mint the stable Full context; Fast requires all actual Draft results", () => {
  const fast = results("pull_request",fastJobs);
  assert.deepEqual(ciResultViolations(fast,"pull_request",true,"fast"),[]);
  for(const job of fastJobs) {
    for(const result of ["failure","cancelled","skipped",undefined]) {
      assert.notDeepEqual(ciResultViolations({...fast,[job]:{result}},"pull_request",true,"fast"),[]);
    }
    const missing={...fast};delete missing[job];
    assert.notDeepEqual(ciResultViolations(missing,"pull_request",true,"fast"),[]);
  }
  assert.notDeepEqual(ciResultViolations(results("pull_request"),"pull_request",true),[]);
  assert.notDeepEqual(ciResultViolations(results("workflow_dispatch"),"workflow_dispatch",null),[]);
  for(const [event,draft,gate] of [["pull_request",false,"fast"],["pull_request",null,"full"],["pull_request","false","full"],["push",true,"full"],["push",null,"fast"],["push",null,"milestone"],["pull_request",false,"unknown"]]) {
    assert.notDeepEqual(ciResultViolations(results(event),event,draft,gate),[]);
  }
  for (const needs of [undefined,null,[],{}, {...results("push"),newJob:{result:"success"}}]) {
    assert.notDeepEqual(ciResultViolations(needs,"push",null),[]);
  }
});

test("workflow keeps exact protected contexts, distinct milestone names and non-skippable Full aggregation", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const blocks=Object.fromEntries([...workflow.split("\njobs:\n")[1].matchAll(/^  ([a-z][a-z-]+):\n([\s\S]*?)(?=^  [a-z][a-z-]+:$|(?![\s\S]))/gmu)].map((m)=>[m[1],m[2]]));
  const jobs=Object.keys(blocks).filter(name=>!["quality","impact","fast","fast-native"].includes(name));
  assert.deepEqual(jobs.sort(),[...requiredJobs].sort());
  const needs=(job)=>blocks[job].match(/needs: \[([^\]]+)\]/u)[1].split(",").map(name=>name.trim()).sort();
  assert.deepEqual(needs("quality"),[...requiredJobs].sort());
  assert.deepEqual(needs("fast"),[...fastJobs].sort());
  assert.deepEqual(needs("integration"),["integration-shards"]);
  assert.match(blocks.quality,/name: .*'Full CI \(milestone, non-merge\)'.*'Unit, race, lint, and release consumers'/u);
  assert.match(blocks.integration,/name: .*'Full real-engine integration \(milestone\)'.*'PostgreSQL and Redis integration'/u);
  for(const gate of ["quality","integration"]) assert.match(blocks[gate],/if: \$\{\{ always\(\) \}\}/u);
  assert.match(blocks.quality,/CI_PR_DRAFT: .*toJSON\(github.event.pull_request.draft\).*'null'/u);
  assert.match(blocks.quality,/CI_GATE: .*'milestone'.*'full'/u);
  assert.match(blocks.integration,/INTEGRATION_RESULT: .*needs.integration-shards.result/u);
  assert.match(blocks.integration,/exit 1/u);
  for(const job of ["race","release-commands-windows","sqlite-windows","coverage-go","coverage","integration-shards"]) {
    assert.match(blocks[job],/github.event_name != 'pull_request' \|\| github.event.pull_request.draft == false/u);
  }
  assert.match(workflow,/converted_to_draft/u);
  assert.match(workflow,/workflow_dispatch:/u);
  assert.doesNotMatch(blocks.quality,/continue-on-error/u);
  assert.doesNotMatch(blocks.integration,/continue-on-error/u);
  assert.match(blocks.fast,/CI_GATE: fast/u);
  assert.match(blocks["fast-native"],/TEST_POSTGRES_DSN:/u);
  assert.match(blocks["fast-native"],/scripts\/ci-fast.mjs/u);
  assert.match(blocks["integration-shards"],/suite: \[kernel, harness, redis, continuation\]/u);
  assert.match(blocks["integration-shards"],/run-integration.mjs --external-services "--suite=\$CI_INTEGRATION_SUITE"/u);
});

test("advisory report is not a correctness dependency", () => {
  const workflow=readFileSync(new URL("../.github/workflows/ci.yml",import.meta.url),"utf8");
  assert.doesNotMatch(workflow,/needs: \[[^\]]*\bimpact\b/u);
});

test("static checks run first without dropping or duplicating any pre-P0 check target", () => {
  const makefile = readFileSync(new URL("../Makefile", import.meta.url), "utf8");
  const targets = (name) => makefile.match(new RegExp(`^${name} := (.+)$`, "mu"))[1].split(/\s+/u);
  const statics = targets("CHECK_STATIC_TARGETS");
  const executions = targets("CHECK_EXECUTION_TARGETS");
  const original = "metadata provider-transport-check agent-baseline-test fmt-check tidy-check integration-compile go-vet go-test go-race go-lint ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check".split(" ");
  assert.deepEqual([...statics, ...executions].sort(), original.sort());
  assert.ok(["go-lint", "ts-lint", "ts-typecheck", "integration-compile"].every((target) => statics.includes(target)));
  assert.ok(!statics.includes("go-test") && !statics.includes("go-race"));
  assert.match(makefile, /CHECK_TARGETS := \$\(CHECK_STATIC_TARGETS\) \$\(CHECK_EXECUTION_TARGETS\)/u);
  assert.match(makefile, /check-no-race: \$\(filter-out go-race,\$\(CHECK_TARGETS\)\)/u);
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const staticStep = workflow.indexOf("- name: Run static checks before behavioral tests");
  const executionStep = workflow.indexOf("- name: Run smoke and release-consumer checks");
  assert.ok(staticStep >= 0 && staticStep < executionStep);
});
