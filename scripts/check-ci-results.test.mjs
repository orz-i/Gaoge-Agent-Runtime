import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { ciResultViolations, requiredJobs } from "./check-ci-results.mjs";

const results = (event) => Object.fromEntries(requiredJobs.map((job) => [job, {
  result: job === "observability" && event === "pull_request" ? "skipped" : "success",
}]));

test("every expected PR/main job must pass; only the existing PR Collector skip is accepted", () => {
  for (const event of ["pull_request", "push"]) {
    assert.deepEqual(ciResultViolations(results(event), event), []);
    for (const job of requiredJobs) {
      for (const result of ["failure", "cancelled", "skipped", "pending", undefined]) {
        if (job === "observability" && event === "pull_request" && result === "skipped") continue;
        const needs = results(event);
        needs[job] = { result };
        assert.notDeepEqual(ciResultViolations(needs, event), [], `${event}/${job}/${result}`);
      }
      const needs = results(event);
      delete needs[job];
      assert.notDeepEqual(ciResultViolations(needs, event), []);
    }
  }
});

test("missing, unknown, and malformed dependency/event data fail closed", () => {
  for (const needs of [undefined, null, [], {}, { ...results("push"), newJob: { result: "success" } }]) {
    assert.notDeepEqual(ciResultViolations(needs, "push"), []);
  }
  assert.notDeepEqual(ciResultViolations(results("push"), "workflow_dispatch"), []);
});

test("the stable required check waits for and validates all CI jobs, including coverage and audit", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const jobs = [...workflow.split("\njobs:\n")[1].matchAll(/^  ([a-z][a-z-]+):$/gmu)]
    .map((match) => match[1]).filter((name) => name !== "quality");
  assert.deepEqual(jobs.sort(), [...requiredJobs].sort());
  const needs = workflow.match(/needs: \[([^\]]+)\]/u)[1].split(",").map((name) => name.trim());
  assert.deepEqual(needs.sort(), [...requiredJobs].sort());
  assert.match(workflow, /name: Unit, race, lint, and release consumers/u);
  assert.match(workflow, /if: \$\{\{ always\(\) \}\}/u);
  assert.match(workflow, /CI_NEEDS_JSON: \$\{\{ toJSON\(needs\) \}\}/u);
  assert.match(workflow, /run: node scripts\/check-ci-results\.mjs/u);
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
