import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { allGoModules, analyzeImpact, dependencyGraph, impactSummary, reverseClosure } from "./ci-impact-policy.mjs";
import { collectImpact, impactContext, parseChangedFiles } from "./ci-impact.mjs";

const readModule = (module) => readFileSync(new URL(`../${module}/go.mod`, import.meta.url), "utf8");
const graph = dependencyGraph(readModule);
const context = { event: "pull_request" };
const change = (file, status = "M") => ({ path: file, status });
const plan = (files) => analyzeImpact(files.map((file) => typeof file === "string" ? change(file) : file), [graph], context);
const sha = (character) => character.repeat(40);
const temporary = (t) => {
  const directory = mkdtempSync(path.join(tmpdir(), "ci-impact-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return directory;
};

test("documentation/TypeScript/Transport suggestions never change actual full execution or coverage policy", () => {
  for (const [files, recommendation] of [
    [["docs/reliability.md"], "documentation"], [["README.md", "CONTRIBUTING.md"], "documentation"],
    [["ts/agent-runtime-client/src/client.ts", "pnpm-lock.yaml"], "typescript"],
    [["go/provider-transport/request.go"], "go"], [["docs/notes.md", "ts/agent-runtime-client/src/client.ts"], "mixed"],
  ]) {
    const result = plan(files);
    assert.equal(result.recommendation, recommendation);
    assert.equal(result.mode, "shadow");
    assert.equal(result.execution, "full");
    assert.equal(result.coveragePolicy, "complete-14-module-union-required");
    assert.ok(!Object.hasOwn(result, "skip") && !Object.hasOwn(result, "runJobs"));
    assert.ok(result.checks.includes("security") && result.checks.includes("codeql"));
  }
  assert.deepEqual(plan(["go/provider-transport/request.go"]).affectedGoModules, ["go/provider-transport"]);
});

test("the longest module owner wins and reverse dependencies include nested examples and detached E2E", () => {
  const nested = plan(["go/agent-runtime-harness/examples/task-agent/main.go"]);
  assert.deepEqual(nested.directGoModules, ["go/agent-runtime-harness/examples/task-agent"]);
  assert.deepEqual(nested.affectedGoModules, nested.directGoModules);
  const provider = plan(["go/agent-runtime-provider/openai/client.go"]);
  assert.deepEqual(provider.affectedGoModules, ["go/agent-runtime-provider", "go/agent-runtime-harness/examples/task-agent"]);
  const harness = plan(["go/agent-runtime-harness/turn.go"]);
  for (const module of ["go/agent-runtime-harness", "go/agent-runtime-harness/examples/task-agent", "go/agent-runtime-harness-postgres", "go/agent-runtime-http"]) {
    assert.ok(harness.affectedGoModules.includes(module), module);
  }
  assert.ok(harness.detachedGoModules.includes("integration/continuation-e2e"));
  assert.ok(harness.checks.includes("real-engine-integration"));
  assert.ok(harness.typescript, "HTTP reverse consumer needs TypeScript wire/packed validation");
  const otel = plan(["go/agent-runtime-otel/otel.go"]);
  assert.deepEqual(otel.detachedGoModules, ["integration/otel-e2e"]);
  assert.ok(otel.checks.includes("collector-contract"));
});

test("old and new graph edges are unioned; missing/unknown graph data cannot become empty scope", () => {
  const old = structuredClone(graph);
  old["go/agent-runtime-harness"].push("go/provider-transport");
  assert.ok(reverseClosure(["go/provider-transport"], [old, graph]).includes("go/agent-runtime-http"));
  const result = analyzeImpact([change("go/provider-transport/request.go")], [old, graph], context);
  assert.ok(result.affectedGoModules.includes("go/agent-runtime-harness"));
  assert.equal(analyzeImpact([change("docs/readme.md")], [], context).recommendation, "full");
  assert.throws(() => reverseClosure(["go/unknown"], [graph]));
  assert.throws(() => reverseClosure([], [{ ...graph, "go/provider-transport": ["go/unknown"] }]));
  assert.throws(() => dependencyGraph((module) => module === "go/provider-transport" ? "module unknown\n" : readModule(module)), /identity/u);
  assert.throws(() => dependencyGraph((module) => readModule(module) + "\nrequire github.com/orz-i/Gaoge-Agent-Runtime/go/unknown v1.0.0\n"), /unknown internal/u);
});

test("Core/persistence, CI/tooling, shared contracts, and dependency/release changes recommend full", () => {
  for (const file of [
    "go/agent-runtime/agent/main.go", "go/agent-runtime/conformance/store.go", "go/agent-runtime/memory/example_test.go",
    "go/agent-runtime-postgres/store.go", "go/agent-runtime-sqlite/store.go", "go/agent-runtime-redis/queue.go",
    "go/agent-runtime-harness-postgres/store.go", "integration/continuation-e2e/recovery_test.go",
    ".github/workflows/ci.yml", ".github/actions/setup-go/action.yml", "scripts/check-release.mjs", "Makefile", "go.work", "go.work.sum",
    "package.json", "pnpm-workspace.yaml", ".golangci.yml", "docker-compose.test.yml",
    "contracts/agent-runtime/v1/openapi.yaml", "contracts/consumers/go/agent-runtime_test.go",
    "docs/a2a.md", "docs/audits/fixtures/example/README.md", "docs/releases/v0.1.0-beta.24.md",
    "VERSION", "CHANGELOG.md", "agent-runtime-boundary.json", "go/provider-transport/VERSION",
    "go/agent-runtime-provider/go.mod", "go/provider-transport/go.sum", "ts/agent-runtime-client/package.json",
  ]) {
    const result = plan([file]);
    assert.equal(result.recommendation, "full", file);
    assert.equal(result.execution, "full");
    assert.equal(plan(["docs/readme.md", file]).recommendation, "full", `mixed ${file}`);
  }
});

test("module/TypeScript READMEs are executable consumer candidates, not generic documentation", () => {
  assert.equal(plan(["ts/agent-runtime-client/README.md"]).recommendation, "typescript");
  assert.equal(plan(["go/agent-runtime-harness/README.md"]).recommendation, "go");
  assert.equal(plan(["go/agent-runtime-sqlite/README.md"]).recommendation, "full");
});

test("unknown paths, empty/malformed/duplicate changes, type changes, and non-PR events recommend full", () => {
  for (const file of ["new-module/main.go", "go/agent-runtime-unknown/main.go", "docs/fixture.json", "benchmarks/new.json",
    "../docs/a.md", "/docs/a.md", "docs/../a.md", "docs//a.md", "docs/a\nb.md", "docs\\a.md"]) {
    assert.equal(plan([file]).recommendation, "full", file);
  }
  for (const changes of [[], null, [null], [change("docs/a.md", "R100")], [change("docs/a.md", "T")], [change("docs/a.md"), change("docs/a.md")]]) {
    assert.equal(analyzeImpact(changes, [graph], context).recommendation, "full");
  }
  for (const event of ["push", "schedule", undefined]) assert.equal(analyzeImpact([change("docs/a.md")], [graph], { event }).recommendation, "full");
  assert.match(impactSummary(plan(["docs/<bad>|name.md"])), /&lt;bad&gt;\\\|/u);
  assert.doesNotMatch(impactSummary(plan(["docs/<bad>|name.md"])), /<bad>/u);
});

test("NUL-delimited diffs are complete beyond API path limits and reject renames/truncation instead of dropping old paths", () => {
  const files = Array.from({ length: 1200 }, (_, index) => `M\0docs/file-${index}.md\0`).join("");
  assert.equal(parseChangedFiles(files).length, 1200);
  assert.deepEqual(parseChangedFiles("D\0docs/old.md\0A\0ts/agent-runtime-client/README.md\0"), [
    change("docs/old.md", "D"), change("ts/agent-runtime-client/README.md", "A"),
  ]);
  for (const text of ["M\0docs/a.md", "M\0", "R100\0old.md\0new.md\0", "M\0../outside.md\0"]) assert.throws(() => parseChangedFiles(text));
});

function collectorFixture({ diff = "M\0docs/notes.md\0", bases = `${sha("d")}\n`, brokenGraph = false } = {}) {
  const calls = [];
  const runGit = (args) => {
    calls.push(args);
    if (args[0] === "rev-parse") return `${sha("c")}\n`;
    if (args[0] === "merge-base") return bases;
    if (args[0] === "diff") return diff;
    if (args[0] === "cat-file") {
      if (brokenGraph) throw new Error("private untrusted stderr must not enter the plan");
      return readModule(args[2].split(":")[1].replace(/\/go\.mod$/u, ""));
    }
    throw new Error("unexpected Git call");
  };
  return { calls, runGit, context: { event: "pull_request", base: sha("a"), head: sha("b"), checkout: sha("c") } };
}

test("collector uses exact SHAs, all merge/base/head/checkout graphs, and non-renaming safe Git argv", () => {
  const fixture = collectorFixture();
  const result = collectImpact(fixture.context, fixture);
  assert.equal(result.recommendation, "documentation");
  assert.equal(result.execution, "full");
  assert.deepEqual(result.context.graphRefs, [sha("d"), sha("a"), sha("b"), sha("c")]);
  const diff = fixture.calls.find((args) => args[0] === "diff");
  for (const flag of ["--no-renames", "--no-ext-diff", "--no-textconv", "-z"]) assert.ok(diff.includes(flag));
  assert.equal(diff.at(-1), "--");
  assert.equal(fixture.calls.filter((args) => args[0] === "cat-file").length, allGoModules.length * 4);
});

test("missing/ambiguous Git data, invalid SHA context and graph errors fail closed without exposing raw errors", () => {
  for (const options of [{ bases: "" }, { bases: `${sha("d")}\n${sha("e")}\n` }, { diff: "M\0truncated" }, { brokenGraph: true }]) {
    const fixture = collectorFixture(options);
    const result = collectImpact(fixture.context, fixture);
    assert.equal(result.recommendation, "full");
    assert.equal(result.execution, "full");
    assert.doesNotMatch(JSON.stringify(result), /private untrusted/u);
  }
  const fixture = collectorFixture();
  assert.equal(collectImpact({ ...fixture.context, base: "main; command" }, fixture).recommendation, "full");
  assert.equal(fixture.calls.length, 0);
  assert.equal(collectImpact({ ...fixture.context, checkout: sha("e") }, fixture).reasons[0], "checkout-commit-mismatch");
  assert.equal(collectImpact({ ...fixture.context, event: "push" }, fixture).reasons[0], "non-pr-always-full");
});

test("event loading keeps only commit context and offers no skip/apply flag", () => {
  const env = { GITHUB_EVENT_NAME: "pull_request", GITHUB_EVENT_PATH: "event.json", GITHUB_SHA: sha("c") };
  const result = impactContext([], env, () => JSON.stringify({ pull_request: { base: { sha: sha("a") }, head: { sha: sha("b") }, body: "private-body" } }));
  assert.deepEqual(result, { event: "pull_request", base: sha("a"), head: sha("b"), checkout: sha("c") });
  assert.deepEqual(impactContext([], env, () => "invalid JSON"), { event: "pull_request" });
  assert.throws(() => impactContext(["--apply"], env), /shadow only/u);
  assert.throws(() => impactContext(["--base", "main", "--head", "HEAD"], env), /full SHA/u);
});

test("real Git rename/deletion analysis includes both old and new owners", (t) => {
  const directory = temporary(t);
  const git = (...args) => execFileSync("git", ["-c", "user.name=CI Fixture", "-c", "user.email=ci@example.invalid",
    "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", ...args],
    { cwd: directory, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  git("init", "-q");
  for (const module of allGoModules) {
    mkdirSync(path.join(directory, module), { recursive: true });
    writeFileSync(path.join(directory, module, "go.mod"), readModule(module));
  }
  mkdirSync(path.join(directory, "docs"));
  writeFileSync(path.join(directory, "docs/old.md"), "document\n");
  writeFileSync(path.join(directory, "go/agent-runtime-provider/deleted_test.go"), "fixture\n");
  git("add", "."); git("commit", "-qm", "base");
  const base = git("rev-parse", "HEAD");
  mkdirSync(path.join(directory, "ts/agent-runtime-client"), { recursive: true });
  renameSync(path.join(directory, "docs/old.md"), path.join(directory, "ts/agent-runtime-client/README.md"));
  rmSync(path.join(directory, "go/agent-runtime-provider/deleted_test.go"));
  git("add", "-A"); git("commit", "-qm", "rename and delete");
  const head = git("rev-parse", "HEAD");
  const result = collectImpact({ event: "pull_request", base, head, checkout: head }, { cwd: directory });
  assert.equal(result.recommendation, "mixed");
  assert.ok(result.changes.some((entry) => entry.path === "docs/old.md" && entry.status === "D"));
  assert.ok(result.changes.some((entry) => entry.path === "ts/agent-runtime-client/README.md" && entry.status === "A"));
  assert.ok(result.affectedGoModules.includes("go/agent-runtime-harness/examples/task-agent"));
});

test("CLI persists an explicit full fallback on missing context, without reading secrets or enabling skips", (t) => {
  const directory = temporary(t);
  const script = fileURLToPath(new URL("./ci-impact.mjs", import.meta.url));
  const result = spawnSync(process.execPath, [script], {
    encoding: "utf8", env: { ...process.env, GITHUB_EVENT_NAME: "pull_request", GITHUB_EVENT_PATH: path.join(directory, "missing"),
      GITHUB_SHA: "", CI_IMPACT_DIRECTORY: directory, GITHUB_STEP_SUMMARY: path.join(directory, "job-summary.md"), PRIVATE_TOKEN: "do-not-report-this" },
  });
  assert.equal(result.status, 0, result.stderr);
  const output = readFileSync(path.join(directory, "plan.json"), "utf8");
  const plan = JSON.parse(output);
  assert.equal(plan.execution, "full");
  assert.equal(plan.recommendation, "full");
  assert.doesNotMatch(output, /do-not-report-this/u);
  assert.match(readFileSync(path.join(directory, "job-summary.md"), "utf8"), /No job or test is skipped/u);
});

test("shadow CI publishes evidence but has no outputs or validation-job consumers; existing full matrices and coverage remain", () => {
  const ci = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const job = ci.split("\n  impact:\n")[1].split("\n  quality-main:\n")[0];
  assert.match(job, /name: CI impact analysis \(shadow\)/u);
  assert.match(job, /fetch-depth: 0/u);
  assert.match(job, /run: node scripts\/ci-impact\.mjs/u);
  assert.match(job, /retention-days: 7/u);
  assert.match(job, /^    continue-on-error: true$/mu);
  assert.match(job, /if-no-files-found: warn/u);
  assert.doesNotMatch(job, /outputs:|GITHUB_OUTPUT|contents: write|pull_request_target|pnpm install|setup-go|node --test/u);
  assert.doesNotMatch(ci, /needs\.impact\.outputs|fromJSON\([^\n]*impact|CI_IMPACT_MODE|paths-ignore:|\n\s+paths:/u);
  assert.match(ci, /needs: \[quality-main, race,/u);
  assert.doesNotMatch(ci, /needs: \[[^\]]*\bimpact\b/u);
  assert.equal([...ci.matchAll(/shard: \[core, sqlite, adapters\]/gu)].length, 2);
  assert.match(ci, /merge-go-coverage\.mjs coverage\/go-shards/u);
  const makefile = readFileSync(new URL("../Makefile", import.meta.url), "utf8");
  assert.match(makefile, /node --test[^\n]*scripts\/ci-impact\.test\.mjs/u);
});
