import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { writeGoCoverageReport } from "./go-coverage-report.mjs";
import { coverageProfileName, modules, selectModules } from "./go-workspace-layout.mjs";
import { measureCI } from "./ci-timings.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export { modules };

const mode = process.argv[2];
const supported = new Set([
  "fmt-check",
  "tidy-check",
  "build",
  "test",
  "race",
  "vet",
  "lint",
  "coverage",
  "vuln",
]);
if (!supported.has(mode)) {
  throw new Error(`usage: node scripts/go-workspace.mjs <${[...supported].join("|")}>`);
}
const { selected, shard } = selectModules(mode, process.argv.slice(3));

const coverageDirectory = path.join(root, "coverage", "go");
const coverageProfiles = [];
if (mode === "coverage") {
  rmSync(coverageDirectory, { recursive: true, force: true });
  mkdirSync(coverageDirectory, { recursive: true });
}

for (const modulePath of selected) {
  measureCI({ kind: "go-module", name: modulePath, mode }, () => {
    const cwd = path.join(root, modulePath);
    console.log(`==> ${mode} ${modulePath}`);
    if (mode === "fmt-check") checkFormatting(cwd);
    if (mode === "tidy-check") checkTidy(cwd, modulePath);
    if (mode === "build") run("go", ["build", "./..."], cwd);
    if (mode === "test") run("go", ["test", "./...", "-count=1"], cwd);
    if (mode === "race") run("go", ["test", "-race", "./...", "-count=1"], cwd);
    if (mode === "vet") run("go", ["vet", "./..."], cwd);
    if (mode === "lint") run("golangci-lint", ["run", "./..."], cwd);
    if (mode === "coverage") {
      const profile = path.join(coverageDirectory, coverageProfileName(modulePath));
      run("go", ["test", "./...", "-count=1", "-covermode=atomic", "-coverpkg=./...", `-coverprofile=${profile}`], cwd);
      coverageProfiles.push(profile);
    }
    if (mode === "vuln") {
      run("go", ["run", "golang.org/x/vuln/cmd/govulncheck@v1.8.0", "./..."], cwd);
    }
  });
}

if (mode === "coverage") {
  if (shard) {
    // A shard never enforces a local floor: only the complete union is valid.
    writeFileSync(path.join(coverageDirectory, "manifest.json"), `${JSON.stringify({
      schemaVersion: 1, shard, commit: process.env.GITHUB_SHA ?? null, modules: selected,
    }, null, 2)}\n`);
  } else {
    writeGoCoverageReport(coverageProfiles.map((name) => ({ name, contents: readFileSync(name, "utf8") })), coverageDirectory);
  }
}

function checkFormatting(cwd) {
  const files = walk(cwd).filter((file) => file.endsWith(".go"));
  const output = run("gofmt", ["-l", ...files], cwd, true);
  if (output.trim()) throw new Error(`gofmt required:\n${output}`);
}

function checkTidy(cwd, modulePath) {
  const snapshots = new Map();
  for (const name of ["go.mod", "go.sum"]) {
    const file = path.join(cwd, name);
    snapshots.set(file, existsSync(file) ? readFileSync(file) : null);
  }
  try {
    const core = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime";
    const harness = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness";
    const provider = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider";
    // Every optional module, including SQLite, must build against the
    // coordinated *source* Core until the next immutable module tags exist.
    // A real no-replace consumer is verified separately by --published.
    const independentTransport = modulePath === "go/provider-transport";
    const replaceCore = !independentTransport && modulePath !== "go/agent-runtime";
    const replaceProvider = !independentTransport && modulePath !== "go/agent-runtime" && modulePath !== "go/agent-runtime-provider";
    const replaceHarness = ["go/agent-runtime-harness-sqlite", "go/agent-runtime-harness-postgres", "go/agent-runtime-http", "go/agent-runtime-harness/examples/task-agent"].includes(modulePath);
    // Harness integration tests exercise this same source cohort's three
    // Kernel Stores; tidy must not resolve unreleased adapters from old tags.
    const sourceAdapters = {
      "go/agent-runtime-harness-postgres": ["agent-runtime-postgres", "agent-runtime-sqlite"],
      "go/agent-runtime-harness-sqlite": ["agent-runtime-harness-postgres", "agent-runtime-sqlite", "agent-runtime-postgres"],
    };
    const adapterReplaces = (sourceAdapters[modulePath] ?? []).map((name) => `github.com/orz-i/Gaoge-Agent-Runtime/go/${name}`);
    const version = readFileSync(path.join(root, "VERSION"), "utf8").trim();
    const localVersions = [replaceCore ? core : null, replaceHarness ? harness : null, replaceProvider ? provider : null, ...adapterReplaces].filter(Boolean)
      .flatMap((name) => [`${name} v${version} `, `${name} v${version}/go.mod `]);
    // go mod tidy with temporary source replaces drops genuine release-tag
    // hashes from go.sum. Preserve exact current-cohort pins for direct
    // GOWORK=off -mod=readonly builds, but still catch every OTHER tidy drift.
    const normalizedSum = (content) => (content?.toString("utf8") ?? "").split("\n")
      .filter((line) => !localVersions.some((prefix) => line.startsWith(prefix)))
      .join("\n");
    if (replaceCore) {
      run("go", ["mod", "edit", `-replace=${core}=${path.relative(cwd, path.join(root, "go", "agent-runtime"))}`], cwd);
    }
    if (replaceProvider) {
      run("go", ["mod", "edit", `-replace=${provider}=${path.relative(cwd, path.join(root, "go", "agent-runtime-provider"))}`], cwd);
    }
    if (replaceHarness) {
      run("go", ["mod", "edit", `-replace=${harness}=${path.relative(cwd, path.join(root, "go", "agent-runtime-harness"))}`], cwd);
    }
    for (const name of adapterReplaces) {
      run("go", ["mod", "edit", `-replace=${name}=${path.relative(cwd, path.join(root, "go", name.split("/").at(-1)))}`], cwd);
    }
    run("go", ["mod", "tidy"], cwd);
    for (const name of adapterReplaces) run("go", ["mod", "edit", `-dropreplace=${name}`], cwd);
    if (replaceCore) run("go", ["mod", "edit", `-dropreplace=${core}`], cwd);
    if (replaceProvider) run("go", ["mod", "edit", `-dropreplace=${provider}`], cwd);
    if (replaceHarness) {
      run("go", ["mod", "edit", `-dropreplace=${harness}`], cwd);
    }
    run("go", ["mod", "edit", "-fmt"], cwd);
    const changed = [...snapshots].filter(([file, before]) => {
      const after = existsSync(file) ? readFileSync(file) : null;
      if (before === null || after === null) return before !== after;
      if (file.endsWith("go.sum")) return normalizedSum(before) !== normalizedSum(after);
      return !before.equals(after);
    });
    if (changed.length > 0) {
      throw new Error(`go mod tidy changed ${changed.map(([file]) => path.relative(root, file)).join(", ")}`);
    }
  } finally {
    for (const [file, content] of snapshots) {
      if (content === null) rmSync(file, { force: true });
      else writeFileSync(file, content);
    }
  }
}

function walk(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const absolute = path.join(directory, entry.name);
    return entry.isDirectory() ? walk(absolute) : [absolute];
  });
}

function run(command, args, cwd, capture = false) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    shell: false,
    stdio: capture ? "pipe" : "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (result.status !== 0) {
    const error = new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
    error.exitCode = result.status ?? 1;
    error.signal = result.signal;
    throw error;
  }
  return capture ? result.stdout ?? "" : "";
}
