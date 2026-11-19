import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { measureCI, timingSummary } from "./ci-timings.mjs";
import { runCITargets } from "./run-ci-targets.mjs";

function fixture(t) {
  const directory = mkdtempSync(path.join(tmpdir(), "ci-timings-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return { directory, env: { CI_TIMING_DIRECTORY: directory, GITHUB_JOB: "test-job" },
    records: () => readFileSync(path.join(directory, "test-job.jsonl"), "utf8").trim().split("\n").map(JSON.parse) };
}

test("timing is opt-in and preserves task return values", () => {
  assert.equal(measureCI({ kind: "target", name: "test" }, () => 42, { env: {}, clock: () => assert.fail("clock should not run") }), 42);
});

test("successes and failures append monotonic timings without raw command arguments or environment", (t) => {
  const { env, records } = fixture(t);
  let time = 10;
  const options = { env: { ...env, PRIVATE_TOKEN: "not-for-reporting" }, clock: () => (time += 25) };
  const label = { kind: "go-module", name: "go/module", mode: "race", argv: ["not-for-reporting"] };
  assert.equal(measureCI(label, () => 42, options), 42);
  const error = Object.assign(new Error("original failure"), { exitCode: 7, signal: "SIGTERM" });
  assert.throws(() => measureCI(label, () => { throw error; }, options), (actual) => actual === error);
  assert.deepEqual(records().map(({ durationMs, exitCode, signal }) => ({ durationMs, exitCode, signal })), [
    { durationMs: 25, exitCode: 0, signal: null }, { durationMs: 25, exitCode: 7, signal: "SIGTERM" },
  ]);
  assert.doesNotMatch(JSON.stringify(records()), /not-for-reporting|original failure/u);
});

test("target runner preserves order/argv and stops immediately on a failed target", () => {
  const calls = [];
  const labels = [];
  const options = { measure: (label, task) => { labels.push(label); return task(); },
    run: (command, args) => { calls.push({ command, args }); if (args[1] === "go-lint") throw new Error("lint failed"); } };
  assert.throws(() => runCITargets(["metadata", "go-lint", "go-test"], options), /lint failed/u);
  assert.deepEqual(calls, [
    { command: "make", args: ["--no-print-directory", "metadata"] },
    { command: "make", args: ["--no-print-directory", "go-lint"] },
  ]);
  assert.deepEqual(labels.map((label) => label.name), ["metadata", "go-lint"]);
  for (const targets of [[], ["--eval=unsafe"], ["go-test;echo unsafe"], ["go-test", "go-test"]]) {
    assert.throws(() => runCITargets(targets, options), /distinct Make target/u);
  }
});

test("command CLI preserves exit status, literal arguments, and launch/signal failures", (t) => {
  const { env, records } = fixture(t);
  const script = fileURLToPath(new URL("./ci-timings.mjs", import.meta.url));
  const run = (command, args) => spawnSync(process.execPath, [script, "--run", "fixture", "--", command, ...args], {
    encoding: "utf8", env: { ...process.env, ...env },
  });
  assert.equal(run(process.execPath, ["-e", "process.exit(7)"]).status, 7);
  const literal = run(process.execPath, ["-e", "console.log(process.argv[1])", "literal; $(not-a-command)"]);
  assert.equal(literal.status, 0, literal.stderr);
  assert.match(literal.stdout, /literal; \$\(not-a-command\)/u);
  assert.notEqual(run(path.join(env.CI_TIMING_DIRECTORY, "missing-executable"), []).status, 0);
  assert.notEqual(run(process.execPath, ["-e", "process.kill(process.pid, 'SIGTERM')"]).status, 0);
  assert.deepEqual(records().map((row) => row.exitCode), [7, 0, 1, 1]);
  assert.equal(records()[3].signal, "SIGTERM");
});

test("summaries retain failed operations, sort slowest modules, and escape labels", (t) => {
  const { directory } = fixture(t);
  writeFileSync(path.join(directory, "fixture.jsonl"), [
    { schemaVersion: 1, kind: "go-module", name: "fast", mode: "test", durationMs: 10, exitCode: 0 },
    { schemaVersion: 1, kind: "go-module", name: "slow|<bad>", mode: "race", durationMs: 2000, exitCode: 7, signal: "SIGTERM" },
    { schemaVersion: 1, kind: "target", name: "go-race", durationMs: 3000, exitCode: 7 },
  ].map((row) => JSON.stringify(row)).join("\n"));
  const summary = timingSummary(directory);
  assert.match(summary, /times overlap/u);
  assert.ok(summary.indexOf("slow\\|&lt;bad>") < summary.indexOf("| fast"));
  assert.match(summary, /FAIL \(7, SIGTERM\)/u);
  assert.match(timingSummary(path.join(directory, "missing")), /No measured commands completed/u);
  writeFileSync(path.join(directory, "invalid.jsonl"), JSON.stringify({ schemaVersion: 99 }));
  assert.throws(() => timingSummary(directory), /invalid CI timing/u);
});

test("summary CLI writes the GitHub step summary without creating a fake timing record", (t) => {
  const { directory, env } = fixture(t);
  const output = path.join(directory, "summary.md");
  const script = fileURLToPath(new URL("./ci-timings.mjs", import.meta.url));
  const result = spawnSync(process.execPath, [script, "--summary"], {
    encoding: "utf8", env: { ...process.env, ...env, GITHUB_STEP_SUMMARY: output },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(readFileSync(output, "utf8"), /No measured commands completed/u);
  assert.equal(existsSync(path.join(directory, "test-job.jsonl")), false);
});

test("workspace timing preserves every module and test/race flag, and records module failures before stopping", (t) => {
  const script = fileURLToPath(new URL("./go-workspace.mjs", import.meta.url));
  for (const mode of ["test", "race"]) {
    for (const fail of [false, true]) {
      const { directory, env, records } = fixture(t);
      const calls = path.join(directory, "calls");
      writeFileSync(path.join(directory, "go"), `#!/bin/sh
printf '%s|%s\\n' "$PWD" "$*" >> "$CALLS"
if [ "$FAIL_MODULE" = 1 ] && [ "\${PWD##*/}" = agent-runtime-provider ]; then exit 9; fi
exit 0
`, { mode: 0o755 });
      const result = spawnSync(process.execPath, [script, mode], {
        encoding: "utf8", env: { ...process.env, ...env, PATH: `${directory}${path.delimiter}${process.env.PATH}`,
          CALLS: calls, FAIL_MODULE: fail ? "1" : "0" },
      });
      assert.equal(result.status, fail ? 1 : 0, result.stderr);
      const rows = records();
      assert.equal(rows.length, fail ? 2 : 13);
      assert.ok(rows.every((row) => row.kind === "go-module" && row.mode === mode));
      assert.equal(rows.at(-1).exitCode, fail ? 9 : 0);
      if (!fail) {
        assert.ok(rows.some((row) => row.name === "go/agent-runtime-harness/examples/task-agent"));
        assert.equal(rows.at(-1).name, "go/provider-transport");
      }
      for (const line of readFileSync(calls, "utf8").trim().split("\n")) {
        assert.equal(line.split("|")[1], mode === "race" ? "test -race ./... -count=1" : "test ./... -count=1");
      }
    }
  }
});

test("CI target lists retain all checks and every measured job publishes even after failure", () => {
  const read = (name) => readFileSync(new URL(`../${name}`, import.meta.url), "utf8");
  const makefile = read("Makefile");
  assert.match(makefile, /ci-static:\n\tnode scripts\/run-ci-targets\.mjs \$\(CHECK_STATIC_TARGETS\)/u);
  assert.match(makefile, /ci-execution-no-race:\n\tnode scripts\/run-ci-targets\.mjs \$\(filter-out go-race,\$\(CHECK_EXECUTION_TARGETS\)\)/u);
  const ci = read(".github/workflows/ci.yml");
  assert.equal([...ci.matchAll(/if: always\(\)\n\s+uses: \.\/\.github\/actions\/upload-ci-timings/gu)].length, 7);
  const action = read(".github/actions/upload-ci-timings/action.yml");
  assert.match(action, /retention-days: 7/u);
  assert.match(action, /github\.run_attempt/u);
  assert.match(read("scripts/go-workspace.mjs"), /measureCI\(\{ kind: "go-module", name: modulePath, mode \}/u);
});
