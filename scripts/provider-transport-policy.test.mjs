import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { transportModule, transportDirectory, transportDependencyViolations, transportMetadataViolations } from "./provider-transport-policy.mjs";

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), "transport-policy-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const directory = path.join(root, transportDirectory);
  mkdirSync(directory, { recursive: true });
  writeFileSync(path.join(directory, "go.mod"), `module ${transportModule}\n\ngo 1.26\n`);
  writeFileSync(path.join(directory, "VERSION"), "0.1.0-beta.1\n");
  writeFileSync(path.join(directory, "README.md"), "Independent transport.\n");
  writeFileSync(path.join(directory, "LICENSE"), "Fixture license.\n");
  writeFileSync(path.join(root, "agent-runtime-boundary.json"), JSON.stringify({ goModules: ["github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime"] }));
  return root;
}

test("independent metadata accepts an independently versioned module", (t) => {
  assert.deepEqual(transportMetadataViolations(fixture(t)), []);
});

test("missing module and invalid version are rejected", (t) => {
  const root = fixture(t);
  writeFileSync(path.join(root, transportDirectory, "VERSION"), "latest\n");
  assert.match(transportMetadataViolations(root).join("\n"), /invalid independent/u);
  rmSync(path.join(root, transportDirectory, "go.mod"));
  assert.match(transportMetadataViolations(root).join("\n"), /missing.*go.mod/u);
});

test("wrong identity, local replace, old source and cohort coupling fail closed", (t) => {
  const root = fixture(t);
  writeFileSync(path.join(root, transportDirectory, "go.mod"), "module example.com/alias\nreplace example.com/dep => ../dep\n");
  mkdirSync(path.join(root, "go/agent-runtime-provider/transport"), { recursive: true });
  writeFileSync(path.join(root, "agent-runtime-boundary.json"), JSON.stringify({ goModules: [transportModule] }));
  const failures = transportMetadataViolations(root).join("\n");
  for (const marker of ["identity", "replace", "cohort", "compatibility"]) assert.ok(failures.includes(marker), marker);
});

test("resolved transitive package and module dependencies cannot hide SDK or product imports", () => {
  for (const dependency of [
    "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model",
    "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness v0.1.0-beta.24",
    "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/transport",
    "github.com/orz-i/Gaoge/backend/internal/conversation",
    "github.com/orz-i/Gaoge-Agent-Server/internal/runtimehost",
    `${transportModule}-bridge`,
    `${transportModule}.hidden`,
  ]) {
    assert.equal(transportDependencyViolations([dependency], []).length, 1);
    assert.equal(transportDependencyViolations([], [dependency]).length, 1);
  }
});

test("generated test-package exceptions cannot hide required modules", () => {
  for (const name of [`${transportModule}.test`, `${transportModule}_test`, `${transportModule}/hidden-module`]) {
    assert.deepEqual(transportDependencyViolations([name], []), []);
    assert.equal(transportDependencyViolations([], [`${name} v0.1.0`]).length, 1);
  }
});

test("independent module tags cannot join the Runtime synchronized publisher", () => {
  const tags = execFileSync(process.execPath, [fileURLToPath(new URL("./release-tags.mjs", import.meta.url))], { encoding: "utf8" });
  assert.doesNotMatch(tags, /go\/provider-transport\//u);
  const workflow = readFileSync(new URL("../.github/workflows/provider-transport.yml", import.meta.url), "utf8");
  assert.match(workflow, /go\/provider-transport\/v\*/u);
  assert.match(workflow, /check-provider-transport\.mjs --published/u);
  assert.match(workflow, /contents: read/u);
  assert.doesNotMatch(workflow, /contents: write|git (?:tag|push)|gh release create/u);
});

test("stdlib, test dependencies and standalone transport packages remain legal", () => {
  assert.deepEqual(transportDependencyViolations(
    ["context", "net/http", transportModule, `${transportModule}.test`, `${transportModule}_test`, `${transportModule}/internal/testfixture`, "github.com/stretchr/testify/assert"],
    [`${transportModule} v0.1.0-beta.1`, "github.com/stretchr/testify v1.12.1"],
  ), []);
});
