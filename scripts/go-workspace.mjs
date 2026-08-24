import { existsSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export const modules = [
  "go/agent-runtime",
  "go/agent-runtime-harness",
  "go/agent-runtime-harness-postgres",
  "go/agent-runtime-mcp",
  "go/agent-runtime-a2a",
  "go/agent-runtime-postgres",
  "go/agent-runtime-redis",
  "go/agent-runtime-http",
];

const mode = process.argv[2];
const supported = new Set(["fmt-check", "tidy-check", "build", "test", "race", "vet", "lint"]);
if (!supported.has(mode)) {
  throw new Error(`usage: node scripts/go-workspace.mjs <${[...supported].join("|")}>`);
}

for (const modulePath of modules) {
  const cwd = path.join(root, modulePath);
  console.log(`==> ${mode} ${modulePath}`);
  if (mode === "fmt-check") checkFormatting(cwd);
  if (mode === "tidy-check") checkTidy(cwd, modulePath);
  if (mode === "build") run("go", ["build", "./..."], cwd);
  if (mode === "test") run("go", ["test", "./...", "-count=1"], cwd);
  if (mode === "race") run("go", ["test", "-race", "./...", "-count=1"], cwd);
  if (mode === "vet") run("go", ["vet", "./..."], cwd);
  if (mode === "lint") run("golangci-lint", ["run", "./..."], cwd);
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
    if (modulePath !== "go/agent-runtime") {
      run("go", ["mod", "edit", `-replace=${core}=../agent-runtime`], cwd);
    }
    if (["go/agent-runtime-harness-postgres", "go/agent-runtime-http"].includes(modulePath)) {
      run("go", ["mod", "edit", `-replace=${harness}=../agent-runtime-harness`], cwd);
    }
    run("go", ["mod", "tidy"], cwd);
    if (modulePath !== "go/agent-runtime") run("go", ["mod", "edit", `-dropreplace=${core}`], cwd);
    if (["go/agent-runtime-harness-postgres", "go/agent-runtime-http"].includes(modulePath)) {
      run("go", ["mod", "edit", `-dropreplace=${harness}`], cwd);
    }
    run("go", ["mod", "edit", "-fmt"], cwd);
    const changed = [...snapshots].filter(([file, before]) => {
      const after = existsSync(file) ? readFileSync(file) : null;
      return before === null ? after !== null : after === null || !before.equals(after);
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
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
  }
  return capture ? result.stdout ?? "" : "";
}
