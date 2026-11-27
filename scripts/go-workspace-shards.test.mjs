import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { allGoModules, coverageProfileName, detachedModules, modules, selectModules, shards, validateLayout } from "./go-workspace-layout.mjs";
import { mergeGoCoverage } from "./merge-go-coverage.mjs";
import { writeGoCoverageReport } from "./go-coverage-report.mjs";

const temporary = (t) => {
  const directory = realpathSync(mkdtempSync(path.join(tmpdir(), "go-shards-test-")));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return directory;
};

test("shards partition all 15 go.work modules exactly once, including the nested example and independent Transport", () => {
  validateLayout();
  const work = readFileSync(new URL("../go.work", import.meta.url), "utf8");
  const listed = [...work.matchAll(/^\s+\.\/(go\/\S+)$/gmu)].map((match) => match[1]);
  assert.deepEqual([...modules].sort(), listed.sort());
  assert.equal(modules.length, 15);
  assert.equal(new Set(modules.map(coverageProfileName)).size, 15);
  assert.deepEqual(detachedModules, ["integration/continuation-e2e", "integration/otel-e2e"]);
  assert.deepEqual(selectModules("tidy-check", []).selected, [...modules, ...detachedModules]);
  assert.equal(new Set(allGoModules).size, 17);
  assert.throws(() => validateLayout({ ...shards, extra: [modules[0]] }), /exactly once/u);
  assert.throws(() => validateLayout({ ...shards, sqlite: [] }), /exactly once/u);
  assert.throws(() => validateLayout({ ...shards, sqlite: ["go/unknown"] }), /exactly once/u);
  for (const mode of ["test", "race", "coverage"]) {
    assert.deepEqual(selectModules(mode, []).selected, modules);
    for (const shard of Object.keys(shards)) assert.deepEqual(selectModules(mode, [`--shard=${shard}`]).selected, shards[shard]);
  }
  for (const [mode, args] of [["lint", ["--shard=core"]], ["coverage", ["--shard=missing"]],
    ["race", ["--shard="]], ["race", ["--shard=core", "--shard=sqlite"]], ["test", ["--run=small"]]]) {
    assert.throws(() => selectModules(mode, args), /known --shard/u);
  }
});

function coverageFixture(t) {
  const directory = temporary(t);
  const source = path.join(directory, "downloaded");
  const output = path.join(directory, "merged");
  for (const [shard, selected] of Object.entries(shards)) {
    const folder = path.join(source, `go-coverage-${shard}`);
    mkdirSync(folder, { recursive: true });
    writeFileSync(path.join(folder, "manifest.json"), JSON.stringify({ schemaVersion: 1, shard, commit: "fixture-sha", modules: selected }));
    for (const module of selected) writeFileSync(path.join(folder, coverageProfileName(module)), `mode: atomic\n${module}/source.go:1.1,2.1 1 1\n`);
  }
  const merge = (options = {}) => mergeGoCoverage(source, output, { commit: "fixture-sha", ...options });
  return { source, output, merge, core: path.join(source, "go-coverage-core"), sqlite: path.join(source, "go-coverage-sqlite") };
}

test("complete coverage uses the original statement union rather than averaging shard percentages", (t) => {
  const f = coverageFixture(t);
  const result = f.merge();
  assert.deepEqual(result, { coveredStatements: 15, totalStatements: 15, percent: 100, minimum: 70 });
  writeFileSync(path.join(f.sqlite, "agent-runtime-sqlite.out"), "mode: atomic\ngo/sqlite/a.go:1.1,2.1 10 1\ngo/sqlite/b.go:1.1,2.1 90 0\n");
  assert.throws(() => f.merge(), /below 70/u);
  const summary = JSON.parse(readFileSync(path.join(f.output, "summary.json"), "utf8"));
  assert.deepEqual(summary, { coveredStatements: 24, totalStatements: 114, percent: 21.05, minimum: 70 });
});

test("blocks repeated across shards are unioned and zero-execution statements remain in the denominator", (t) => {
  const f = coverageFixture(t);
  writeFileSync(path.join(f.core, "agent-runtime.out"), "mode: atomic\nshared.go:1.1,2.1 3 0\nzero.go:1.1,2.1 2 0\n");
  writeFileSync(path.join(f.sqlite, "agent-runtime-sqlite.out"), "mode: atomic\nshared.go:1.1,2.1 3 5\n");
  assert.deepEqual(f.merge(), { coveredStatements: 16, totalStatements: 18, percent: 88.89, minimum: 70 });
});

test("missing, extra, stale, wrongly assigned, or partial shards cannot pass the aggregate floor", (t) => {
  for (const mutate of [
    (f) => rmSync(f.sqlite, { recursive: true }),
    (f) => mkdirSync(path.join(f.source, "go-coverage-unexpected")),
    (f) => rmSync(path.join(f.core, "agent-runtime.out")),
    (f) => writeFileSync(path.join(f.core, "extra.out"), "mode: atomic\n"),
    (f) => writeFileSync(path.join(f.core, "manifest.json"), JSON.stringify({ schemaVersion: 1, shard: "core", commit: "older-sha", modules: shards.core })),
    (f) => writeFileSync(path.join(f.core, "manifest.json"), JSON.stringify({ schemaVersion: 1, shard: "core", commit: "fixture-sha", modules: [modules[0]] })),
    (f) => writeFileSync(path.join(f.core, "agent-runtime.out"), "mode: count\na.go:1.1,2.1 1 1\n"),
    (f) => writeFileSync(path.join(f.core, "agent-runtime.out"), "mode: atomic\ninvalid\n"),
  ]) {
    const f = coverageFixture(t);
    mutate(f);
    assert.throws(() => f.merge());
  }
});

test("the unsharded and merged reporter retain the 70 percent floor and fail on invalid overrides", (t) => {
  const profiles = [{ name: "fixture", contents: "mode: atomic\na.go:1.1,2.1 7 1\nb.go:1.1,2.1 3 0\n" }];
  const directory = temporary(t);
  assert.equal(writeGoCoverageReport(profiles, directory).percent, 70);
  assert.throws(() => writeGoCoverageReport(profiles, directory, 71), /below 71/u);
  for (const value of ["not-a-number", -1, 101]) assert.throws(() => writeGoCoverageReport(profiles, directory, value), /invalid GO_COVERAGE_MIN/u);
});

test("the real workspace CLI runs complete test/race/atomic coverage argv and never writes a successful partial manifest", (t) => {
  const directory = temporary(t);
  const scripts = path.join(directory, "scripts");
  mkdirSync(scripts);
  for (const name of ["go-workspace.mjs", "go-workspace-layout.mjs", "go-coverage-report.mjs", "go-coverage-summary.mjs", "ci-timings.mjs"]) {
    copyFileSync(new URL(`./${name}`, import.meta.url), path.join(scripts, name));
  }
  for (const module of allGoModules) {
    mkdirSync(path.join(directory, module), { recursive: true });
    writeFileSync(path.join(directory, module, "go.mod"), "module example.com/fixture\n");
    writeFileSync(path.join(directory, module, "go.sum"), "fixture hash\n");
  }
  writeFileSync(path.join(directory, "VERSION"), "0.1.0-beta.24\n");
  const bin = path.join(directory, "bin");
  mkdirSync(bin);
  writeFileSync(path.join(bin, "go"), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.CALLS, JSON.stringify({ cwd: process.cwd(), args, gowork: process.env.GOWORK }) + '\\n');
if (process.env.FAIL_MODULE === path.basename(process.cwd())) process.exit(9);
if (args[0] === 'mod' && args[1] === 'tidy' && process.env.DRIFT_MODULE === path.basename(process.cwd())) {
  fs.appendFileSync('go.mod', '\\n// stale module\\n');
  fs.appendFileSync('go.sum', 'new hash\\n');
}
const profile = args.find(arg => arg.startsWith('-coverprofile='));
if (profile) fs.writeFileSync(profile.slice('-coverprofile='.length), 'mode: atomic\\nfixture/' + path.basename(process.cwd()) + '.go:1.1,2.1 1 1\\n');
`, { mode: 0o755 });
  if (process.platform === "win32") {
    // CreateProcess resolves go.exe, while Unix executes the fixture shebang.
    // Node receives "test" as its script path and forwards the remaining Go arguments.
    copyFileSync(process.execPath, path.join(bin, "go.exe"));
    for (const module of allGoModules) {
      for (const command of ["test", "mod"]) {
        writeFileSync(path.join(directory, module, command),
          `process.argv.splice(2, 0, ${JSON.stringify(command)}); require(${JSON.stringify(path.join(bin, "go"))});`);
      }
    }
  }
  const calls = path.join(directory, "calls");
  const run = (mode, args = [], fail = "", drift = "") => {
    writeFileSync(calls, "");
    return spawnSync(process.execPath, [path.join(scripts, "go-workspace.mjs"), mode, ...args], {
      encoding: "utf8", env: { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, CALLS: calls,
        FAIL_MODULE: fail, DRIFT_MODULE: drift, GOWORK: path.join(directory, "go.work"), GITHUB_SHA: "fixture-sha", GO_COVERAGE_MIN: "70", CI_TIMING_DIRECTORY: "" },
    });
  };
  for (const mode of ["race", "coverage"]) {
    for (const shard of Object.keys(shards)) {
      const result = run(mode, [`--shard=${shard}`]);
      assert.equal(result.status, 0, result.stderr);
      const commands = readFileSync(calls, "utf8").trim().split("\n").map(JSON.parse);
      assert.deepEqual(commands.map((command) => path.relative(directory, command.cwd).split(path.sep).join("/")), shards[shard]);
      for (const { args } of commands) {
        if (mode === "race") assert.deepEqual(args, ["test", "-race", "./...", "-count=1"]);
        else assert.deepEqual(args.slice(0, -1), ["test", "./...", "-count=1", "-covermode=atomic", "-coverpkg=./..."]);
      }
      if (mode === "coverage") {
        const manifest = JSON.parse(readFileSync(path.join(directory, "coverage/go/manifest.json"), "utf8"));
        assert.deepEqual(manifest.modules, shards[shard]);
        assert.equal(manifest.commit, "fixture-sha");
        assert.ok(!existsSync(path.join(directory, "coverage/go/summary.json")));
      }
    }
  }
  // Static dependency validation covers the same detached module identities
  // used by impact analysis, but never changes their source replacements.
  const tidy = run("tidy-check");
  assert.equal(tidy.status, 0, tidy.stderr);
  const tidyCommands = readFileSync(calls, "utf8").trim().split("\n").map(JSON.parse);
  assert.deepEqual(tidyCommands.filter(({ args }) => args.join(" ") === "mod tidy")
    .map(({ cwd }) => path.relative(directory, cwd).split(path.sep).join("/")), allGoModules);
  for (const module of detachedModules) {
    assert.deepEqual(tidyCommands.filter(({ cwd }) => cwd === path.join(directory, module))
      .map(({ args, gowork }) => ({ args, gowork })), [
      { args: ["mod", "tidy"], gowork: "off" },
      { args: ["mod", "edit", "-fmt"], gowork: "off" },
    ]);
    const result = run("tidy-check", [], "", path.basename(module));
    assert.equal(result.status, 1, result.stderr);
    assert.ok(result.stderr.includes(`go mod tidy changed ${module}/go.mod, ${module}/go.sum`), result.stderr);
    assert.equal(readFileSync(path.join(directory, module, "go.mod"), "utf8"), "module example.com/fixture\n");
    assert.equal(readFileSync(path.join(directory, module, "go.sum"), "utf8"), "fixture hash\n");
  }
  assert.equal(run("coverage", ["--shard=core"], "agent-runtime").status, 1);
  assert.ok(!existsSync(path.join(directory, "coverage/go/manifest.json")));
  assert.equal(run("coverage").status, 0);
  assert.equal(JSON.parse(readFileSync(path.join(directory, "coverage/go/summary.json"), "utf8")).minimum, 70);
});

test("CI runs each full coverage/race shard, collects complete artifacts, and removes only redundant ordinary test invocations", () => {
  const read = (name) => readFileSync(new URL(`../${name}`, import.meta.url), "utf8");
  const ci = read(".github/workflows/ci.yml");
  const makefile = read("Makefile");
  const matrixes = [...ci.matchAll(/shard: \[([^\]]+)\]/gu)].map((match) => match[1].split(", "));
  assert.deepEqual(matrixes, [Object.keys(shards), Object.keys(shards)]);
  assert.equal([...ci.matchAll(/fail-fast: false/gu)].length, 3);
  assert.match(ci, /coverage --shard=\$\{\{ matrix.shard \}\}/u);
  assert.match(ci, /race --shard=\$\{\{ matrix.shard \}\}/u);
  assert.match(ci, /cache-scope: race-\$\{\{ matrix.shard \}\}/u);
  assert.match(ci, /cache-scope: coverage-\$\{\{ matrix.shard \}\}/u);
  assert.match(ci, /name: go-coverage-\$\{\{ matrix.shard \}\}/u);
  assert.match(ci, /pattern: go-coverage-\*/u);
  assert.match(ci, /needs: \[coverage-go\]/u);
  assert.match(ci, /merge-go-coverage\.mjs coverage\/go-shards/u);
  assert.match(ci, /run-ci-targets\.mjs ts-coverage/u);
  assert.doesNotMatch(ci, /make ci-execution-no-race|make ci-coverage|GO_COVERAGE_MIN|--run=|continue-on-error: \$\{/u);
  assert.match(makefile, /ci-consumers:\n\tnode scripts\/run-ci-targets\.mjs \$\(filter-out go-test go-race ts-test,\$\(CHECK_EXECUTION_TARGETS\)\)/u);
  assert.match(read("ts/agent-runtime-client/package.json"), /"coverage": "vitest run --coverage"/u);
  assert.match(read(".github/workflows/release.yml"), /make check && make coverage && make security && make integration-test/u);
});

test("matrix artifact names are distinct and build caches cannot multiply per PR commit or cross mode/shard boundaries", () => {
  const cache = readFileSync(new URL("../.github/actions/setup-go/action.yml", import.meta.url), "utf8");
  const timing = readFileSync(new URL("../.github/actions/upload-ci-timings/action.yml", import.meta.url), "utf8");
  assert.match(timing, /github.job \}\}\$\{\{ inputs.suffix \}\}/u);
  assert.match(cache, /modules-key=\$base-modules-v2-\$DEPENDENCIES_HASH/u);
  assert.doesNotMatch(cache, /modules-key=.*(?:SOURCE_SHA|CACHE_SCOPE)/u);
  assert.match(cache, /build-key=\$base-build-\$CACHE_SCOPE-v2-\$DEPENDENCIES_HASH-\$SOURCE_SHA/u);
  assert.match(cache, /build-fallback=\$base-build-\$CACHE_SCOPE-v2-/u);
  assert.match(cache, /if: github.event_name == 'push' && github.ref == 'refs\/heads\/main'\n\s+uses: actions\/cache@v5/u);
  assert.match(cache, /if: github.event_name != 'push' \|\| github.ref != 'refs\/heads\/main'\n\s+uses: actions\/cache\/restore@v5/u);
  assert.equal([...cache.matchAll(/path: \$\{\{ steps.paths.outputs.build \}\}/gu)].length, 2);
  assert.equal([...cache.matchAll(/path: \$\{\{ steps.paths.outputs.modules \}\}/gu)].length, 1);
});
