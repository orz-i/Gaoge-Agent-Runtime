import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("../.github/actions/setup-sqlite/ensure-sqlite.sh", import.meta.url));

function runSetup(t, { status, failCommand = "", installLeavesMissing = false, queryFails = false } = {}) {
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
  const result = spawnSync("bash", [script], {
    encoding: "utf8",
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

test("all existing Linux SQLite users share the checked setup, including the release workflow", () => {
  const read = (name) => readFileSync(new URL(`../.github/workflows/${name}.yml`, import.meta.url), "utf8");
  const ci = read("ci");
  const release = read("release");
  assert.equal([...ci.matchAll(/uses: \.\/\.github\/actions\/setup-sqlite/gu)].length, 4);
  assert.equal([...release.matchAll(/uses: \.\/\.github\/actions\/setup-sqlite/gu)].length, 1);
  assert.doesNotMatch(ci + release, /apt-get/u);
});
