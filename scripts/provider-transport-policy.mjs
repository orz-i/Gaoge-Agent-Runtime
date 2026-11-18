import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

export const transportModule = "github.com/orz-i/Gaoge-Agent-Runtime/go/provider-transport";
export const transportDirectory = "go/provider-transport";

// Test the resolved graph as well as source metadata. Merely renaming an import
// does not prove independence from Kernel, Harness or product modules.
export function transportDependencyViolations(packagePaths, modulePaths) {
  const violations = [];
  const entries = [
    ...packagePaths.map((name) => [name, true]),
    ...modulePaths.map((name) => [name, false]),
  ];
  for (const [dependency, isPackage] of entries) {
    const name = dependency.split(/\s/u)[0];
    if (!name || name === transportModule) continue;
    // Go synthesizes these test packages, not independently required modules.
    if (isPackage && (name === `${transportModule}.test` ||
        name === `${transportModule}_test` || name.startsWith(`${transportModule}/`))) continue;
    if (name === "github.com/orz-i/Gaoge" || name.startsWith("github.com/orz-i/Gaoge/") ||
        name === "github.com/orz-i/Gaoge-Agent-Server" || name.startsWith("github.com/orz-i/Gaoge-Agent-Server/") ||
        name === "github.com/orz-i/Gaoge-Agent-Runtime" || name.startsWith("github.com/orz-i/Gaoge-Agent-Runtime/")) {
      violations.push(`Provider Transport depends on execution/product package ${name}`);
    }
  }
  return violations;
}

export function transportMetadataViolations(root) {
  const violations = [];
  const directory = path.join(root, transportDirectory);
  for (const name of ["go.mod", "VERSION", "README.md", "LICENSE"]) {
    if (!existsSync(path.join(directory, name))) violations.push(`missing ${transportDirectory}/${name}`);
  }
  if (violations.length > 0) return violations;
  const module = readFileSync(path.join(directory, "go.mod"), "utf8");
  if (module.match(/^module\s+(\S+)/mu)?.[1] !== transportModule) violations.push("incorrect standalone module identity");
  if (/^replace(?:\s|\()/mu.test(module)) violations.push("standalone module must not contain replace directives");
  const version = readFileSync(path.join(directory, "VERSION"), "utf8").trim();
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/u.test(version)) violations.push("invalid independent module VERSION");
  const boundary = JSON.parse(readFileSync(path.join(root, "agent-runtime-boundary.json"), "utf8"));
  if (boundary.goModules.includes(transportModule)) violations.push("independent transport must not join the Runtime release cohort");
  if (existsSync(path.join(root, "go/agent-runtime-provider/transport"))) violations.push("retired transport source or compatibility package remains");
  return violations;
}
