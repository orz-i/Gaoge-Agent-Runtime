import { execFileSync } from "node:child_process";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { analyzeImpact, dependencyGraph, fullImpact, impactSummary, validChangedPath } from "./ci-impact-policy.mjs";

const validSha = (value) => typeof value === "string" && /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/u.test(value);

export function parseChangedFiles(output) {
  if (typeof output !== "string" || (output && !output.endsWith("\0"))) throw new Error("truncated diff");
  if (!output) return [];
  const parts = output.slice(0, -1).split("\0");
  if (parts.length % 2) throw new Error("incomplete diff record");
  const changes = [];
  for (let index = 0; index < parts.length; index += 2) {
    const status = parts[index];
    const file = parts[index + 1];
    if (!/^[ACDMTUXB]$/u.test(status) || !validChangedPath(file)) throw new Error("unrecognized diff record");
    changes.push({ status, path: file });
  }
  return changes;
}

function git(args, cwd) {
  return execFileSync("git", args, { cwd, encoding: "utf8", maxBuffer: 32 * 1024 * 1024,
    stdio: ["ignore", "pipe", "pipe"] });
}

export function collectImpact({ event, base, head, checkout }, { cwd = process.cwd(), runGit = (args) => git(args, cwd) } = {}) {
  const context = { event, base: validSha(base) ? base : null, head: validSha(head) ? head : null,
    checkout: validSha(checkout) ? checkout : null };
  let changes = [];
  if (!validSha(head) || !validSha(checkout) || (event === "pull_request" && !validSha(base))) {
    return fullImpact("missing-or-invalid-commit-context", changes, context);
  }
  try {
    const actual = runGit(["rev-parse", "--verify", "HEAD"]).trim();
    if (actual !== checkout) return fullImpact("checkout-commit-mismatch", changes, context);
    if (event !== "pull_request") return fullImpact("non-pr-always-full", changes, context);
    const bases = runGit(["merge-base", "--all", base, head]).trim().split("\n");
    if (bases.length !== 1 || !validSha(bases[0])) return fullImpact("missing-or-ambiguous-merge-base", changes, context);
    context.mergeBase = bases[0];
    changes = parseChangedFiles(runGit(["diff", "--name-status", "--no-renames", "--no-ext-diff", "--no-textconv", "-z", bases[0], head, "--"]));
    const refs = [...new Set([bases[0], base, head, checkout])];
    const graphs = refs.map((ref) => dependencyGraph((module) => runGit(["cat-file", "blob", `${ref}:${module}/go.mod`])));
    context.graphRefs = refs;
    return analyzeImpact(changes, graphs, context);
  } catch {
    // No API pagination limit or partial diff is accepted as a complete plan.
    // Do not leak Git stderr, source contents, credentials, or event bodies.
    return fullImpact("diff-or-dependency-analysis-unavailable", changes, context);
  }
}

export function impactContext(args, env = process.env, read = (file) => readFileSync(file, "utf8")) {
  if (args.length) {
    if (args.length !== 4 || args[0] !== "--base" || args[2] !== "--head" || !validSha(args[1]) || !validSha(args[3])) {
      throw new Error("usage: node scripts/ci-impact.mjs [--base <full SHA> --head <full SHA>] (shadow only)");
    }
    return { event: "pull_request", base: args[1], head: args[3], checkout: env.GITHUB_SHA ?? args[3] };
  }
  try {
    const event = env.GITHUB_EVENT_NAME;
    const payload = JSON.parse(read(env.GITHUB_EVENT_PATH));
    return { event, base: payload.pull_request?.base?.sha, head: payload.pull_request?.head?.sha ?? payload.after,
      checkout: env.GITHUB_SHA };
  } catch {
    return { event: env.GITHUB_EVENT_NAME };
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const plan = collectImpact(impactContext(process.argv.slice(2)));
    const directory = process.env.CI_IMPACT_DIRECTORY ?? path.resolve("artifacts/ci-impact");
    const summary = impactSummary(plan);
    mkdirSync(directory, { recursive: true });
    writeFileSync(path.join(directory, "plan.json"), `${JSON.stringify(plan, null, 2)}\n`);
    writeFileSync(path.join(directory, "summary.md"), summary);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
    console.log(`Shadow impact: ${plan.recommendation}; actual execution: full; changed files: ${plan.changes.length}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
