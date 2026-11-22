import path from "node:path";
import { fileURLToPath } from "node:url";

export const requiredJobs = [
  "quality-main", "race", "release-commands-windows", "sqlite-windows", "coverage-go", "coverage", "security", "integration", "observability",
];

export function ciResultViolations(needs, event) {
  if (!["pull_request", "push"].includes(event)) return ["unexpected CI event"];
  if (!needs || typeof needs !== "object" || Array.isArray(needs)) return ["missing CI job results"];
  const violations = Object.keys(needs).filter((job) => !requiredJobs.includes(job))
    .map((job) => `unrecognized CI dependency: ${job}`);
  for (const job of requiredJobs) {
    const expected = job === "observability" && event === "pull_request" ? "skipped" : "success";
    if (needs[job]?.result !== expected) {
      violations.push(`${job}: expected ${expected}, received ${needs[job]?.result ?? "missing"}`);
    }
  }
  return violations;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const violations = ciResultViolations(JSON.parse(process.env.CI_NEEDS_JSON ?? "null"), process.env.GITHUB_EVENT_NAME);
    if (violations.length) throw new Error(violations.join("\n"));
    console.log("All required CI jobs passed; only PR Collector E2E may be skipped.");
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
