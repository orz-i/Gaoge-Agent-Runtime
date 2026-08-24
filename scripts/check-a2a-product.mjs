import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const coreRoot = path.join(root, "go", "agent-runtime");
const a2aRoot = path.join(root, "go", "agent-runtime-a2a");
const violations = [];

for (const file of goFiles(coreRoot)) {
  if (file.endsWith("_test.go")) continue;
  const source = readFileSync(file, "utf8");
  if (source.includes("github.com/a2aproject") || source.includes("agent-runtime-a2a")) {
    violations.push(`${relative(file)} couples the protocol-neutral runtime to A2A`);
  }
}

for (const file of goFiles(a2aRoot)) {
  const relativePath = relative(file);
  if (file.endsWith("_test.go") || relativePath.includes("/internal/tcksut/")) continue;
  const source = readFileSync(file, "utf8");
  for (const forbidden of ["net.Listen(", "http.ListenAndServe(", "http.Serve("]) {
    if (source.includes(forbidden)) violations.push(`${relativePath} opens a listener with ${forbidden}`);
  }
}

requireText("go/agent-runtime-a2a/plugin.go", "CapabilityPlugin kernel.Capability = \"protocol.a2a\"");
requireText("go/agent-runtime-a2a/plugin.go", "TargetPrefix     string            = \"a2a:\"");
requireText("go/agent-runtime-a2a/client.go", "ProtocolVersion    = \"1.0\"");
requireText("go/agent-runtime-a2a/client.go", "a2asdk.TransportProtocolHTTPJSON");
requireText("go/agent-runtime-a2a/host.go", "ProtocolBinding: a2asdk.TransportProtocolHTTPJSON");
requireText("docs/a2a.md", "The dependency direction is fixed");
requireText("docs/a2a.md", "## Beta.2 support matrix");
requireText("scripts/run-a2a-tck.mjs", "5996b79f9cefa6fc390980e383e358a66fb9e49e");

if (violations.length > 0) {
  console.error(violations.map((item) => `- ${item}`).join("\n"));
  process.exit(1);
}

run("go", ["test", "./...", "-count=1"], a2aRoot);
console.log("A2A product boundary and regression gate passed");

function requireText(file, expected) {
  if (!readFileSync(path.join(root, file), "utf8").includes(expected)) {
    violations.push(`${file} is missing required product invariant: ${expected}`);
  }
}

function goFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) return goFiles(absolute);
    return entry.isFile() && entry.name.endsWith(".go") ? [absolute] : [];
  });
}

function relative(file) {
  return path.relative(root, file).replaceAll("\\", "/");
}

function run(command, args, cwd) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    shell: false,
    stdio: "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
  }
}
