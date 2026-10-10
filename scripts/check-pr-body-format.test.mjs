import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { pullRequestBodyFormatViolations } from "./check-pr-body-format.mjs";

const checker = fileURLToPath(new URL("./check-pr-body-format.mjs", import.meta.url));

test("rejects the PR style with literal escaped Markdown separators", () => {
  const body = ["## Scope\\n- Agent feature\\n- Runtime safety\\n\\n## Validation\\n- Tests passed"];
  assert.match(pullRequestBodyFormatViolations(body[0]).join(" "), /escaped newlines/u);
});

test("accepts actual Markdown line breaks", () => {
  const body = ["## Scope", "- Agent feature", "- Runtime safety", "", "## Validation", "- Tests passed"].join("\n");
  assert.deepEqual(pullRequestBodyFormatViolations(body), []);
});

test("allows literal newline escapes in fenced or inline code", () => {
  const tick = String.fromCharCode(96);
  const fence = tick.repeat(3);
  const body = [
    "## Scope", "- Explain a JSON string.", "",
    fence + "js", 'const sample = "first\\nsecond";', fence,
    "", "## Validation", "- Parsed an inline " + tick + '"first\\nsecond"' + tick + " expression.",
  ].join("\n");
  assert.deepEqual(pullRequestBodyFormatViolations(body), []);
});

test("accepts body-file and rejects malformed GitHub PR event payload", () => {
  const dir = mkdtempSync(join(tmpdir(), "runtime-pr-format-"));
  try {
    const path = join(dir, "pr.md");
    writeFileSync(path, "## Scope\n- Real newlines\n\n## Validation\n- Pass\n");
    const file = spawnSync(process.execPath, [checker, "--body-file", path], { encoding: "utf8" });
    assert.equal(file.status, 0, file.stderr);

    const event = join(dir, "github-event.json");
    writeFileSync(event, JSON.stringify({ pull_request: { body: "## Scope\\n- Escaped\\n\\n## Validation\\n- Tests" } }));
    const invalid = spawnSync(process.execPath, [checker], {
      encoding: "utf8", env: { ...process.env, GITHUB_EVENT_PATH: event },
    });
    assert.equal(invalid.status, 1);
    assert.match(invalid.stderr, /escaped newlines/u);

    writeFileSync(event, JSON.stringify({ push: {} }));
    const nonPR = spawnSync(process.execPath, [checker], {
      encoding: "utf8", env: { ...process.env, GITHUB_EVENT_PATH: event },
    });
    assert.equal(nonPR.status, 0, nonPR.stderr);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
