import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (relative) => readFileSync(path.join(root, relative), "utf8");
const version = read("VERSION").trim();
const boundary = JSON.parse(read("agent-runtime-boundary.json"));
const sqlite = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite";

test("SQLite joins one coordinated eleven-module Beta cohort", () => {
  assert.equal(boundary.version, version);
  assert.equal(boundary.goModules.length, 11);
  assert.ok(boundary.goModules.includes(sqlite));
  for (const name of boundary.goModules) {
    const mod = read(name.replace("github.com/orz-i/Gaoge-Agent-Runtime/", "") + "/go.mod");
    assert.match(mod, new RegExp("^module " + name.replaceAll(".", "\\.") + "$", "m"));
    assert.doesNotMatch(mod, /^replace(?:\s|\()/m, "published modules cannot contain path replacements");
    for (const [, , requiredVersion] of mod.matchAll(/^\s*(github\.com\/orz-i\/Gaoge-Agent-Runtime\/go\/[a-z-]+)\s+(v[^\s]+)/gm)) {
      assert.equal(requiredVersion, "v" + version);
    }
  }
});

test("Harness release module stays vendor-neutral and isolates Provider-powered demo", () => {
  const coreHarness = read("go/agent-runtime-harness/go.mod");
  const demo = read("go/agent-runtime-harness/examples/task-agent/go.mod");
  assert.doesNotMatch(coreHarness, /github\.com\/orz-i\/Gaoge-Agent-Runtime\/go\/agent-runtime-provider/);
  assert.match(demo, /github\.com\/orz-i\/Gaoge-Agent-Runtime\/go\/agent-runtime-provider/);
  assert.ok(!boundary.goModules.includes("github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/examples/task-agent"));
});

test("release tag set includes SQLite at precisely the same version", () => {
  const output = execFileSync(process.execPath, ["scripts/release-tags.mjs", "--version", version], {
    cwd: root, encoding: "utf8",
  });
  const lines = output.split("\n");
  const tags = lines.filter((line) => line === "v" + version || line.startsWith("go/") && line.endsWith("/v" + version));
  assert.equal(tags.length, 12); // one root + eleven modules
  assert.ok(tags.includes("go/agent-runtime-sqlite/v" + version));
});

test("publisher verifies actual tagged modules without source replacements", () => {
  const script = read("scripts/check-release.mjs");
  const workflow = read(".github/workflows/release.yml");
  assert.match(script, /const replacements = published \? "" : modules/);
  assert.match(script, /GOWORK: "off"/);
  assert.match(script, /if \(!published\) checkTypeScriptConsumer\(\)/);
  assert.ok(read("contracts/consumers/go/agent-runtime-sqlite_test.go").includes("TestStandaloneSQLiteConsumerPersistsSnapshotAndRelation"));
  const fullGate = workflow.indexOf("- name: Run Beta gate");
  const published = workflow.indexOf("- name: Verify published Go module versions without local replacements");
  const release = workflow.indexOf("- name: Create GitHub prerelease");
  assert.ok(fullGate >= 0 && fullGate < published && published < release);
  assert.match(workflow, /node scripts\/check-release\.mjs --published/);
  assert.match(workflow, /GOWORK=off GOFLAGS=-mod=readonly go test \.\/\.\.\. -count=1/);
});
