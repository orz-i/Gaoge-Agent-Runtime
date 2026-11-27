import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("../.github/actions/setup-sqlite/ensure-sqlite.sh", import.meta.url));
const releaseWorkflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
const releaseBootstrap = releaseWorkflow.split("      - name: Ensure SQLite development dependencies\n        shell: bash\n        run: |\n")[1]
  ?.split("\n      - name: Install dependencies")[0].split("\n").map((line) => line.slice(10)).join("\n").trim();

function runSetup(t, { status, failCommand = "", installLeavesMissing = false, queryFails = false, source } = {}) {
  const directory = mkdtempSync(path.join(tmpdir(), "ci-sqlite-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const state = path.join(directory, "state");
  const calls = path.join(directory, "calls");
  if (status) writeFileSync(state, status);
  writeFileSync(path.join(directory, "dpkg-query"), `#!/usr/bin/env bash
if [[ "\${QUERY_FAILS:-}" == 1 ]]; then exit 2; fi
[[ -f "$STATE" ]] || exit 1
cat "$STATE"
`, { mode: 0o755 });
  writeFileSync(path.join(directory, "sudo"), `#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$CALLS"
[[ "$*" != "$FAIL_COMMAND" ]] || exit 23
case "$*" in
  'apt-get update') ;;
  'apt-get install -y --no-install-recommends libsqlite3-dev')
    if [[ "$INSTALL_LEAVES_MISSING" != 1 ]]; then printf 'install ok installed' > "$STATE"; fi ;;
  *) exit 90 ;;
esac
`, { mode: 0o755 });
  const setup = source === undefined ? script : path.join(directory, "workflow-bootstrap.sh");
  if (source !== undefined) writeFileSync(setup, source);
  const result = spawnSync("bash", [setup], {
    cwd: directory, encoding: "utf8",
    env: { ...process.env, PATH: `${directory}${path.delimiter}${process.env.PATH}`, STATE: state, CALLS: calls,
      FAIL_COMMAND: failCommand, INSTALL_LEAVES_MISSING: installLeavesMissing ? "1" : "0", QUERY_FAILS: queryFails ? "1" : "0" },
  });
  return { ...result, calls: existsSync(calls) ? readFileSync(calls, "utf8").trim().split("\n") : [] };
}

test("preinstalled dependencies do not invoke apt or sudo", (t) => {
  const result = runSetup(t, { status: "install ok installed" });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.calls, []);
  assert.match(result.stdout, /skipping apt/u);
});

test("missing or partially removed dependencies are installed and verified", (t) => {
  for (const status of [undefined, "deinstall ok config-files"]) {
    const result = runSetup(t, { status });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.calls, ["apt-get update", "apt-get install -y --no-install-recommends libsqlite3-dev"]);
  }
});

test("update and install failures propagate without continuing", (t) => {
  const update = runSetup(t, { failCommand: "apt-get update" });
  assert.equal(update.status, 23);
  assert.deepEqual(update.calls, ["apt-get update"]);
  const install = runSetup(t, { failCommand: "apt-get install -y --no-install-recommends libsqlite3-dev" });
  assert.equal(install.status, 23);
  assert.equal(install.calls.length, 2);
});

test("a false-successful install or failed package query cannot bypass dependency verification", (t) => {
  for (const options of [{ installLeavesMissing: true }, { status: "install ok installed", queryFails: true }]) {
    const result = runSetup(t, options);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /not installed after apt/u);
    assert.equal(result.calls.length, 2);
  }
});

test("CI shares the checked setup while release bootstrap is independent of the tag's files", () => {
  const read = (name) => readFileSync(new URL(`../.github/workflows/${name}.yml`, import.meta.url), "utf8");
  const ci = read("ci");
  assert.equal([...ci.matchAll(/uses: \.\/\.github\/actions\/setup-sqlite/gu)].length, 5);
  assert.doesNotMatch(ci, /apt-get/u);
  assert.doesNotMatch(releaseWorkflow, /uses: \.\//u, "old tags need not contain newly introduced local actions");
  assert.equal(releaseBootstrap, readFileSync(script, "utf8").replace(/^#![^\n]*\n/u, "").trim(),
    "inline release bootstrap and the CI action must retain identical behavior");
});

test("the workflow's inline bootstrap runs in an old-tag checkout without any local action/script", (t) => {
  assert.ok(releaseBootstrap);
  for (const options of [
    { status: "install ok installed", code: 0, count: 0 },
    { code: 0, count: 2 },
    { failCommand: "apt-get update", code: 23, count: 1 },
    { failCommand: "apt-get install -y --no-install-recommends libsqlite3-dev", code: 23, count: 2 },
    { installLeavesMissing: true, code: 1, count: 2 },
  ]) {
    const result = runSetup(t, { ...options, source: releaseBootstrap });
    assert.equal(result.status, options.code, result.stderr);
    assert.equal(result.calls.length, options.count);
  }
});
