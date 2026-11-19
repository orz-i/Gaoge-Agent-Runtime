import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { activateDescriptionCheck, descriptionCheck, requiredChecksWithDescription } from "./activate-pr-description-check.mjs";

const quality = "Unit, race, lint, and release consumers";
const initial = () => ({
  strict: true,
  contexts: [quality, "review", "custom check"],
  checks: [{ context: quality, app_id: 15368 }, { context: "review", app_id: 15368 }, { context: "custom check", app_id: null }],
});

test("activation only appends the app-bound check and preserves strictness and unrelated checks", () => {
  const current = initial();
  const before = structuredClone(current);
  const result = requiredChecksWithDescription(current);
  assert.deepEqual(current, before);
  assert.deepEqual(result, { strict: true, checks: [...current.checks, { context: descriptionCheck, app_id: 15368 }] });
  assert.deepEqual(requiredChecksWithDescription({ ...current, checks: result.checks }), result);
});

test("unknown or conflicting branch-protection configurations fail closed", () => {
  assert.throws(() => requiredChecksWithDescription({ ...initial(), contexts: ["missing binding"] }), /unrecognized/u);
  assert.throws(() => requiredChecksWithDescription({ ...initial(), checks: [] }), /unrecognized/u);
  assert.throws(() => requiredChecksWithDescription({ ...initial(), checks: [{ context: quality, app_id: null }], contexts: [quality] }), /trusted app/u);
  assert.throws(() => requiredChecksWithDescription({ ...initial(), checks: [...initial().checks, { context: descriptionCheck, app_id: 7 }] }), /different app/u);
});

function fixture({ workflowMissing = false, conclusion = "success", mergeSha = "merge-sha", extraCheck = false, appChanged = false, mergeChecks = 0, mergeStatuses = [] } = {}) {
  const calls = [];
  const api = (endpoint, options = {}) => {
    calls.push({ endpoint, ...options });
    if (endpoint === "repos/owner/repo") return { default_branch: "main" };
    if (endpoint.includes("/contents/")) {
      if (workflowMissing) throw new Error("workflow is not on main");
      return { encoding: "base64", content: Buffer.from(`on:\n  pull_request:\njobs:\n  description:\n    name: ${descriptionCheck}\n`).toString("base64") };
    }
    if (endpoint.includes("/pulls?")) return [{ number: 1, head: { sha: "head-sha" }, merge_commit_sha: mergeSha }];
    if (endpoint.includes("/check-runs?filter=latest&per_page=1")) return { total_count: mergeChecks, check_runs: [] };
    if (endpoint.includes("/status?")) return { statuses: mergeStatuses };
    if (endpoint.includes("/check-runs?")) return { check_runs: [
      { id: 1, name: descriptionCheck, status: "completed", conclusion: "success", app: { id: 15368 } },
      { id: 2, name: descriptionCheck, status: "completed", conclusion, app: { id: 15368 } },
    ] };
    if (endpoint.endsWith("/required_status_checks")) {
      if (options.method === "PATCH") return options.body;
      const current = initial();
      if (appChanged && calls.filter((call) => call.endpoint === endpoint).length > 1) current.checks[0].app_id = 99;
      if (extraCheck && calls.filter((call) => call.endpoint === endpoint).length > 1) {
        current.checks.push({ context: "new concurrent check", app_id: 8 });
        current.contexts.push("new concurrent check");
      }
      return current;
    }
    throw new Error(`unexpected API call: ${endpoint}`);
  };
  return { api, calls };
}

test("dry run is read-only; apply writes only required status checks after all readiness checks", () => {
  const dry = fixture();
  assert.equal(activateDescriptionCheck("owner/repo", { api: dry.api }).apply, false);
  assert.ok(dry.calls.every((call) => !call.method));
  assert.ok(dry.calls.some((call) => call.endpoint.includes("/commits/head-sha/check-runs?check_name=")));
  const live = fixture({ extraCheck: true });
  activateDescriptionCheck("owner/repo", { api: live.api, apply: true });
  const writes = live.calls.filter((call) => call.method);
  assert.equal(writes.length, 1);
  assert.equal(writes[0].method, "PATCH");
  assert.ok(writes[0].endpoint.endsWith("/required_status_checks"));
  assert.ok(writes[0].body.checks.some((check) => check.context === "new concurrent check"));
});

test("test-merge checks/statuses take precedence over head checks when present", () => {
  for (const options of [{ mergeChecks: 1 }, { mergeStatuses: [{ context: "external check" }] }]) {
    const { api, calls } = fixture(options);
    activateDescriptionCheck("owner/repo", { api });
    assert.ok(calls.some((call) => call.endpoint.includes("/commits/merge-sha/check-runs?check_name=")));
    assert.ok(!calls.some((call) => call.endpoint.includes("/commits/head-sha/")));
  }
  const { api } = fixture({ mergeChecks: null });
  assert.throws(() => activateDescriptionCheck("owner/repo", { api, apply: true }), /unrecognized status source/u);
});

test("an unmerged workflow, a missing merge commit, or a newer failed check prevents every write", () => {
  for (const options of [{ workflowMissing: true }, { mergeSha: null }, { conclusion: "failure" }, { conclusion: "skipped" }, { conclusion: "cancelled" }, { appChanged: true }]) {
    const { api, calls } = fixture(options);
    assert.throws(() => activateDescriptionCheck("owner/repo", { api, apply: true }));
    assert.ok(calls.every((call) => !call.method));
  }
});

test("readiness paginates open PRs instead of checking only the first page", () => {
  const { api: base, calls } = fixture();
  const pages = [];
  const api = (endpoint, options) => {
    if (endpoint.includes("/pulls?")) {
      pages.push(endpoint);
      return endpoint.endsWith("page=1") ? Array.from({ length: 100 }, (_, i) => ({ number: i + 1, merge_commit_sha: "merge-sha", head: { sha: "head-sha" } })) : [];
    }
    return base(endpoint, options);
  };
  assert.equal(activateDescriptionCheck("owner/repo", { api }).pullRequests, 100);
  assert.equal(pages.length, 2);
  assert.ok(calls.every((call) => !call.method));
});

test("the standalone workflow is read-only and the old required gate remains during staged rollout", () => {
  const workflow = readFileSync(new URL("../.github/workflows/pr-description.yml", import.meta.url), "utf8");
  const ci = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  assert.match(workflow, /types: \[opened, reopened, synchronize, edited, ready_for_review\]/u);
  assert.match(workflow, /group: pr-description-/u);
  assert.match(workflow, /timeout-minutes: 3/u);
  assert.doesNotMatch(workflow, /pull_request_target|write|pnpm install|setup-go/u);
  assert.match(ci, /types: \[opened, reopened, synchronize, edited, ready_for_review\]/u);
  assert.match(ci, /run: node scripts\/check-pr-body-format\.mjs/u);
});
