import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const descriptionCheck = "Pull request Markdown description";
const qualityCheck = "Unit, race, lint, and release consumers";

// Preserve every existing check and its app binding. Never replace the whole
// branch-protection document or silently migrate an unfamiliar configuration.
export function requiredChecksWithDescription(current) {
  if (typeof current.strict !== "boolean" || !Array.isArray(current.checks) ||
      !Array.isArray(current.contexts) || current.contexts.some((context) =>
        !current.checks.some((check) => check.context === context))) {
    throw new Error("unrecognized required-check configuration; no changes made");
  }
  const appId = current.checks.find((check) => check.context === qualityCheck)?.app_id;
  if (!Number.isSafeInteger(appId) || appId <= 0) {
    throw new Error("the existing quality check must be bound to its trusted app");
  }
  const checks = current.checks.map(({ context, app_id }) => ({ context, app_id }));
  const existing = checks.find((check) => check.context === descriptionCheck);
  if (existing && existing.app_id !== appId) {
    throw new Error("description check is bound to a different app; no changes made");
  }
  if (!existing) checks.push({ context: descriptionCheck, app_id: appId });
  return { strict: current.strict, checks };
}

function github(endpoint, { method = "GET", body } = {}) {
  const args = ["api", endpoint, "--method", method];
  if (body) args.push("--input", "-");
  return JSON.parse(execFileSync("gh", args, {
    encoding: "utf8", input: body ? JSON.stringify(body) : undefined,
    stdio: ["pipe", "pipe", "inherit"],
  }));
}

export function activateDescriptionCheck(repository, { apply = false, api = github } = {}) {
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/u.test(repository)) {
    throw new Error("provide an explicit owner/repository");
  }
  const base = `repos/${repository}`;
  const branch = api(base).default_branch;
  if (typeof branch !== "string" || !branch) throw new Error("missing default branch");
  // A workflow present only on the feature branch must not become required.
  const workflow = api(`${base}/contents/.github/workflows/pr-description.yml?ref=${encodeURIComponent(branch)}`);
  const source = Buffer.from(workflow.content ?? "", "base64").toString("utf8");
  if (workflow.encoding !== "base64" || !source.includes(`name: ${descriptionCheck}`) ||
      !/^  pull_request:$/mu.test(source)) {
    throw new Error("merge the standalone description workflow to the default branch first");
  }
  const endpoint = `${base}/branches/${encodeURIComponent(branch)}/protection/required_status_checks`;
  const proposed = requiredChecksWithDescription(api(endpoint));
  const appId = proposed.checks.find((check) => check.context === descriptionCheck).app_id;
  let pullRequests = 0;
  for (let page = 1; ; page++) {
    const pulls = api(`${base}/pulls?state=open&per_page=100&page=${page}`);
    for (const pull of pulls) {
      // Strict protection evaluates the test-merge commit when it has checks.
      if (!pull.merge_commit_sha) throw new Error(`PR #${pull.number}: no current test-merge commit; refresh the branch first`);
      const result = api(`${base}/commits/${pull.merge_commit_sha}/check-runs?check_name=${encodeURIComponent(descriptionCheck)}&filter=latest&per_page=100`);
      const latest = result.check_runs.filter((check) => check.name === descriptionCheck && check.app?.id === appId)
        .sort((a, b) => b.id - a.id)[0];
      if (latest?.status !== "completed" || latest.conclusion !== "success") {
        throw new Error(`PR #${pull.number}: description check must pass on the current test-merge commit before activation`);
      }
      pullRequests++;
    }
    if (pulls.length < 100) break;
  }
  // Re-read immediately before a possible write so newly added checks survive.
  const current = api(endpoint);
  const body = requiredChecksWithDescription(current);
  if (body.checks.find((check) => check.context === descriptionCheck).app_id !== appId) {
    throw new Error("trusted check app changed during activation; re-run readiness checks");
  }
  const changed = !current.checks.some((check) => check.context === descriptionCheck);
  if (apply && changed) api(endpoint, { method: "PATCH", body });
  return { repository, branch, apply, changed, pullRequests, ...body };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    if (args.length < 1 || args.length > 2 || (args[1] && args[1] !== "--apply")) {
      throw new Error("usage: node scripts/activate-pr-description-check.mjs owner/repository [--apply]");
    }
    console.log(JSON.stringify(activateDescriptionCheck(args[0], { apply: args[1] === "--apply" }), null, 2));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
