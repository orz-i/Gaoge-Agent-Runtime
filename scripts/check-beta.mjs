import { existsSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const self = path.resolve(fileURLToPath(import.meta.url));
const version = read("VERSION").trim();
const boundary = JSON.parse(read("agent-runtime-boundary.json"));
const packageJSON = JSON.parse(read("ts/agent-runtime-client/package.json"));
const expectedModules = [
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-mcp",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis",
  "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http",
];
const violations = [];

if (version !== "0.1.0-beta.3") violations.push(`unexpected VERSION ${version}`);
if (boundary.version !== version) violations.push("boundary version does not match VERSION");
if (JSON.stringify(boundary.goModules) !== JSON.stringify(expectedModules)) {
  violations.push("boundary Go module list is not the canonical ordered list");
}
if (boundary.typescriptPackage !== "@orz-i/agent-runtime-client") {
  violations.push("boundary TypeScript package is not canonical");
}
if (boundary.openAPI !== "contracts/agent-runtime/v1/openapi.yaml") {
  violations.push("boundary OpenAPI path is not repository-relative");
}

for (const modulePath of expectedModules) {
  const relative = modulePath.replace("github.com/orz-i/Gaoge-Agent-Runtime/", "");
  const moduleFile = path.join(relative, "go.mod");
  if (!existsSync(path.join(root, moduleFile))) {
    violations.push(`missing ${moduleFile}`);
    continue;
  }
  const declaration = read(moduleFile).match(/^module\s+(\S+)/mu)?.[1];
  if (declaration !== modulePath) violations.push(`${moduleFile} declares ${declaration ?? "nothing"}`);
}

if (packageJSON.name !== boundary.typescriptPackage || packageJSON.version !== version) {
  violations.push("TypeScript name/version does not match boundary metadata");
}
if (packageJSON.private === true || packageJSON.publishConfig?.access !== "public") {
  violations.push("TypeScript package is not configured for public publication");
}
for (const field of ["description", "license", "repository", "homepage", "bugs", "engines"]) {
  if (!packageJSON[field]) violations.push(`TypeScript package is missing ${field}`);
}
if (packageJSON.license === "UNLICENSED") violations.push("a public license has not been selected");

for (const required of [
  "LICENSE",
  "README.md",
  "CHANGELOG.md",
  "SECURITY.md",
  "SUPPORT.md",
  "CONTRIBUTING.md",
  "contracts/agent-runtime/v1/openapi.yaml",
  "go/agent-runtime-postgres/real_postgres_test.go",
  "go/agent-runtime-harness-postgres/real_postgres_test.go",
  "go/agent-runtime-redis/real_redis_test.go",
]) {
  if (!existsSync(path.join(root, required))) violations.push(`missing required release file ${required}`);
}

for (const file of walk(root)) {
  const relative = path.relative(root, file).replaceAll("\\", "/");
  if (path.resolve(file) === self) continue;
  const source = readFileSync(file, "utf8");
  if (source.includes("github.com/orz-i/Gaoge/sdk/go/")) violations.push(`${relative} contains retired Go module identity`);
  if (source.includes("@gaoge/agent-runtime-client")) violations.push(`${relative} contains retired TypeScript package identity`);
}

if (violations.length > 0) {
  console.error(violations.map((item) => `- ${item}`).join("\n"));
  process.exit(1);
}
console.log(`Agent Runtime ${version} Beta metadata gate passed`);

function read(relative) {
  return readFileSync(path.join(root, relative), "utf8");
}

function walk(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory() && [".git", "dist", "node_modules"].includes(entry.name)) return [];
    return entry.isDirectory() ? walk(absolute) : [absolute];
  });
}
