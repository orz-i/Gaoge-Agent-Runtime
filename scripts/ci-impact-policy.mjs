import { modules, shards } from "./go-workspace-layout.mjs";

export const detachedModules = ["integration/continuation-e2e", "integration/otel-e2e"];
export const allGoModules = [...modules, ...detachedModules];
const prefix = "github.com/orz-i/Gaoge-Agent-Runtime/";
const identities = new Map(allGoModules.map((module) => [module.startsWith("go/") ? prefix + module :
  `example.com/agent-runtime-${module.split("/").at(-1)}`, module]));
const persistent = new Set(["go/agent-runtime", "go/agent-runtime-postgres", "go/agent-runtime-sqlite",
  "go/agent-runtime-redis", "go/agent-runtime-harness-postgres", "go/agent-runtime-harness-sqlite", "integration/continuation-e2e"]);
const ownerOf = (file) => [...allGoModules].sort((a, b) => b.length - a.length).find((module) => file.startsWith(`${module}/`));

export function validChangedPath(file) {
  return typeof file === "string" && file.length > 0 && !/[\x00-\x1f\x7f\\]/u.test(file) &&
    !file.startsWith("/") && file.split("/").every((segment) => segment && segment !== "." && segment !== "..");
}

// Over-approximate declared internal requires/replaces, including test and
// indirect requirements. Unsupported identities fail closed, never disappear.
export function dependencyGraph(readModule) {
  const graph = Object.fromEntries(allGoModules.map((module) => [module, []]));
  for (const module of allGoModules) {
    const source = readModule(module).replace(/\/\/[^\r\n]*/gu, "");
    const identity = /^module\s+([^\s"]+)\s*$/mu.exec(source)?.[1];
    if (identities.get(identity) !== module) throw new Error("unknown module identity");
    for (const dependency of source.matchAll(/github\.com\/orz-i\/Gaoge-Agent-Runtime\/go\/[A-Za-z0-9_./-]+/gu)) {
      const target = identities.get(dependency[0]);
      if (!target) throw new Error("unknown internal module dependency");
      if (target !== module && !graph[module].includes(target)) graph[module].push(target);
    }
  }
  return graph;
}

export function reverseClosure(direct, graphs) {
  if (!Array.isArray(direct) || direct.some((module) => !allGoModules.includes(module)) || !Array.isArray(graphs) || !graphs.length) {
    throw new Error("missing or invalid dependency graphs/modules");
  }
  for (const graph of graphs) {
    if (!graph || Object.keys(graph).length !== allGoModules.length || allGoModules.some((module) =>
      !Array.isArray(graph[module]) || graph[module].some((dependency) => !allGoModules.includes(dependency)))) {
      throw new Error("incomplete dependency graph");
    }
  }
  const affected = new Set(direct);
  let changed = true;
  while (changed) {
    changed = false;
    for (const module of allGoModules) {
      if (!affected.has(module) && graphs.some((graph) => graph[module].some((dependency) => affected.has(dependency)))) {
        affected.add(module);
        changed = true;
      }
    }
  }
  return allGoModules.filter((module) => affected.has(module));
}

export function fullImpact(reason, changes = [], context = {}) {
  return {
    schemaVersion: 1, mode: "shadow", execution: "full", context,
    changes, recommendation: "full", reasons: [reason],
    directGoModules: [], affectedGoModules: modules, detachedGoModules: detachedModules,
    affectedShards: Object.keys(shards), typescript: true,
    checks: ["metadata", "static", "go-unit-and-race", "go-consumers", "typescript-and-packed-consumer",
      "real-engine-integration", "collector-policy-unchanged", "security", "codeql", "dependency-review", "description"],
    coveragePolicy: "complete-14-module-union-required",
  };
}

export function analyzeImpact(changes, graphs, context = {}) {
  if (!Array.isArray(changes) || !changes.length) return fullImpact("empty-or-unavailable-diff", [], context);
  if (changes.some((change) => !change || !["A", "C", "D", "M", "T", "U", "X", "B"].includes(change.status) || !validChangedPath(change.path))) {
    return fullImpact("invalid-change-record", [], context);
  }
  if (new Set(changes.map((change) => change.path)).size !== changes.length) return fullImpact("duplicate-change-record", changes, context);
  if (context.event !== "pull_request") return fullImpact("non-pr-always-full", changes, context);
  const direct = new Set();
  const kinds = new Set();
  const checks = new Set(["metadata", "security", "codeql", "dependency-review", "description"]);
  for (const { path: file, status } of changes) {
    if (!["A", "D", "M"].includes(status)) return fullImpact("ambiguous-file-status", changes, context);
    if (file.startsWith(".github/") || file.startsWith("scripts/") ||
        ["Makefile", "go.work", "go.work.sum", ".golangci.yml", "package.json", "pnpm-workspace.yaml", "docker-compose.test.yml"].includes(file)) {
      return fullImpact("global-ci-or-tooling", changes, context);
    }
    if (file === "VERSION" || file === "agent-runtime-boundary.json" || file === "CHANGELOG.md" || file === "ts/agent-runtime-client/package.json" ||
        file.startsWith("docs/releases/") || /(?:^|\/)(?:go\.mod|go\.sum|VERSION)$/u.test(file)) {
      return fullImpact("dependency-or-release-boundary", changes, context);
    }
    if (file.startsWith("contracts/") || file === "docs/a2a.md" || file.startsWith("docs/audits/fixtures/")) {
      return fullImpact("shared-or-executable-contract", changes, context);
    }
    const owner = ownerOf(file);
    if (owner) {
      if (persistent.has(owner)) return fullImpact("core-or-persistence-risk", changes, context);
      direct.add(owner);
      kinds.add("go");
    } else if (file.startsWith("ts/agent-runtime-client/") || file === "pnpm-lock.yaml") {
      kinds.add("typescript");
    } else if ((file.startsWith("docs/") && file.endsWith(".md")) ||
        ["README.md", "CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "SECURITY.md", "SUPPORT.md"].includes(file)) {
      kinds.add("documentation");
    } else {
      return fullImpact("unknown-path", changes, context);
    }
  }
  let closure;
  try {
    closure = reverseClosure([...direct], graphs);
  } catch {
    return fullImpact("dependency-analysis-unavailable", changes, context);
  }
  const affectedGoModules = modules.filter((module) => closure.includes(module));
  const detachedGoModules = detachedModules.filter((module) => closure.includes(module));
  if (closure.length) {
    checks.add("static");
    checks.add("go-unit-and-race");
    checks.add("go-consumers");
  }
  if (closure.includes("integration/continuation-e2e") || closure.some((module) => persistent.has(module))) checks.add("real-engine-integration");
  if (closure.includes("integration/otel-e2e")) checks.add("collector-contract");
  const typescript = kinds.has("typescript") || closure.includes("go/agent-runtime-http");
  if (typescript) {
    checks.add("static");
    checks.add("typescript-and-packed-consumer");
  }
  return {
    schemaVersion: 1, mode: "shadow", execution: "full", context, changes,
    recommendation: kinds.size === 1 ? [...kinds][0] : "mixed",
    reasons: ["advisory-only-not-a-skip-decision"], directGoModules: allGoModules.filter((module) => direct.has(module)),
    affectedGoModules, detachedGoModules,
    affectedShards: Object.keys(shards).filter((shard) => shards[shard].some((module) => affectedGoModules.includes(module))),
    typescript, checks: [...checks], coveragePolicy: "complete-14-module-union-required",
  };
}

const escape = (value) => String(value).replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;")
  .replace(/[\\`*_{}\[\]()#+.!|]/gu, "\\$&").replace(/[\r\n]/gu, " ");

export function impactSummary(plan) {
  return `## CI impact analysis (shadow only)\n\n` +
    `**Actual execution: full. No job or test is skipped by this report.**\n\n` +
    `Advisory category: ${escape(plan.recommendation)}. Reasons: ${plan.reasons.map(escape).join(", ")}.\n\n` +
    `Focused Go candidates: ${plan.affectedGoModules.map(escape).join(", ") || "none"}.\n\n` +
    `Detached E2E candidates: ${plan.detachedGoModules.map(escape).join(", ") || "none"}.\n\n` +
    `TypeScript candidate: ${plan.typescript ? "yes" : "no"}. Coverage still requires the complete 14-module union.\n\n` +
    `| Changed path (up to 60) | Status |\n| --- | --- |\n` +
    plan.changes.slice(0, 60).map((change) => `| ${escape(change.path)} | ${escape(change.status)} |\n`).join("") +
    `\nChanged files: ${plan.changes.length}. Full plan is in the JSON artifact; this is not a performance/skip guarantee.\n`;
}
