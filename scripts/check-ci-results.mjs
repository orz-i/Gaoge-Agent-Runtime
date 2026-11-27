import path from "node:path";
import { fileURLToPath } from "node:url";

export const requiredJobs = [
  "quality-main", "race", "release-commands-windows", "sqlite-windows", "coverage-go", "coverage", "security", "integration-shards", "integration", "observability",
];
export const fastJobs = ["quality-main", "security", "fast-native"];

export function ciResultViolations(needs, event, draft, gate = "full") {
  if (!["pull_request", "push", "workflow_dispatch"].includes(event) ||
      (event === "pull_request" ? typeof draft !== "boolean" : draft != null)) return ["missing or unexpected CI event context"];
  if (!["fast", "full", "milestone"].includes(gate)) return ["unknown CI gate"];
  if ((gate === "milestone") !== (event === "workflow_dispatch")) return ["manual Full is a milestone, not the required merge context"];
  if (gate === "fast" && (event !== "pull_request" || !draft)) return ["Fast CI requires a Draft PR"];
  // A skipped required GitHub job can appear successful. Never allow the
  // required aggregate to report success on Draft, even if all needs say success.
  if (gate === "full" && event === "pull_request" && draft) return ["Draft: Full CI required before merge; use Fast CI for task feedback"];
  if (!needs || typeof needs !== "object" || Array.isArray(needs)) return ["missing CI job results"];
  const expectedJobs = gate === "fast" ? fastJobs : requiredJobs;
  const violations = Object.keys(needs).filter((job) => !expectedJobs.includes(job))
    .map((job) => `unrecognized CI dependency: ${job}`);
  for (const job of expectedJobs) {
    const expected = job === "observability" && event !== "push" ? "skipped" : "success";
    if (needs[job]?.result !== expected) {
      violations.push(`${job}: expected ${expected}, received ${needs[job]?.result ?? "missing"}`);
    }
  }
  return violations;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const gate = process.env.CI_GATE;
    if (!gate) throw new Error("CI gate is required");
    const violations = ciResultViolations(JSON.parse(process.env.CI_NEEDS_JSON ?? "null"), process.env.GITHUB_EVENT_NAME,
      JSON.parse(process.env.CI_PR_DRAFT ?? "null"), gate);
    if (violations.length) throw new Error(violations.join("\n"));
    console.log(gate === "fast" ? "Draft Fast CI passed; not Full merge authorization." :
      "All required Full CI jobs passed; Collector E2E is mandatory on main pushes.");
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
