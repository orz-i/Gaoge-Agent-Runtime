import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const version = readFileSync(path.join(root, "VERSION"), "utf8").trim();
const temporary = mkdtempSync(path.join(tmpdir(), "gaoge-agent-runtime-release-"));
const modules = [
  "agent-runtime",
  "agent-runtime-harness",
  "agent-runtime-harness-postgres",
  "agent-runtime-mcp",
  "agent-runtime-a2a",
  "agent-runtime-postgres",
  "agent-runtime-redis",
  "agent-runtime-http",
];
const consumers = path.join(root, "contracts", "consumers");

try {
  for (const name of modules) checkGoConsumer(name);
  checkTypeScriptConsumer();
  console.log(`Agent Runtime ${version} clean-consumer release gate passed`);
} finally {
  rmSync(temporary, { recursive: true, force: true });
}

function checkGoConsumer(name) {
  const consumer = path.join(temporary, name);
  mkdirSync(consumer);
  const replacements = modules
    .map((moduleName) => `replace github.com/orz-i/Gaoge-Agent-Runtime/go/${moduleName} => ${JSON.stringify(path.join(root, "go", moduleName).replaceAll("\\", "/"))}`)
    .join("\n");
  writeFileSync(path.join(consumer, "go.mod"), `module example.com/${name}-consumer\n\ngo 1.26\n\nrequire github.com/orz-i/Gaoge-Agent-Runtime/go/${name} v${version}\n\n${replacements}\n`);
  writeFileSync(path.join(consumer, "runtime_test.go"), readFileSync(path.join(consumers, "go", `${name}_test.go`)));
  run("go", ["mod", "tidy"], consumer, { ...process.env, GOWORK: "off" });
  run("go", ["test", "./..."], consumer, { ...process.env, GOWORK: "off" });
  const dependencies = run("go", ["list", "-deps", "-test", "./..."], consumer, { ...process.env, GOWORK: "off" }, true);
  if (dependencies.includes("github.com/orz-i/Gaoge/backend")) {
    throw new Error(`${name} external consumer unexpectedly depends on the Gaoge host`);
  }
}

function checkTypeScriptConsumer() {
  const packageRoot = path.join(root, "ts", "agent-runtime-client");
  run("pnpm", ["test"], packageRoot);
  run("pnpm", ["build"], packageRoot);
  const packed = JSON.parse(run("npm", ["pack", "--json", "--pack-destination", temporary], packageRoot, process.env, true));
  if (!Array.isArray(packed) || packed.length !== 1) throw new Error("npm pack returned an unexpected manifest");
  const archivePath = path.join(temporary, packed[0].filename);
  if (!existsSync(archivePath)) throw new Error("npm pack did not create an archive");
  const files = new Set((packed[0].files ?? []).map((entry) => entry.path));
  for (const required of ["dist/index.js", "dist/index.d.ts", "LICENSE", "README.md", "package.json"]) {
    if (!files.has(required)) throw new Error(`TypeScript package is missing ${required}`);
  }
  for (const file of files) {
    if (file.includes(".test.") || file.startsWith("src/")) throw new Error(`TypeScript package leaked development file ${file}`);
  }
  const packageJSON = JSON.parse(readFileSync(path.join(packageRoot, "package.json"), "utf8"));
  if (packageJSON.dependencies && Object.keys(packageJSON.dependencies).length > 0) {
    throw new Error("TypeScript client must have zero runtime dependencies");
  }

  const consumer = path.join(temporary, "ts-consumer");
  mkdirSync(consumer);
  writeFileSync(path.join(consumer, "package.json"), JSON.stringify({ name: "runtime-ts-consumer", private: true, type: "module" }));
  for (const name of ["index.mjs", "index.ts"]) {
    writeFileSync(path.join(consumer, name), readFileSync(path.join(consumers, "typescript", name)));
  }
  const fixtureRoot = path.join(root, "contracts", "agent-runtime", "v1", "fixtures");
  const fixtureCases = JSON.parse(readFileSync(path.join(fixtureRoot, "manifest.json"), "utf8"));
  const fixtureTypes = [...new Set(fixtureCases.map((entry) => entry.typescript).filter(Boolean))];
  const declarations = [`import type { ${fixtureTypes.join(", ")} } from "@orz-i/agent-runtime-client";`];
  mkdirSync(path.join(consumer, "fixtures"));
  for (const [index, fixture] of fixtureCases.entries()) {
    const raw = readFileSync(path.join(fixtureRoot, fixture.file), "utf8");
    // Emit JSON literals so tsc checks required fields and literal unions against
    // the packed declarations. JSON.parse (any) would silently bypass this gate.
    if (fixture.typescript) {
      declarations.push(`export const fixture${index}: ${fixture.typescript} = ${JSON.stringify(JSON.parse(raw))};`);
    }
    writeFileSync(path.join(consumer, "fixtures", fixture.file), raw);
  }
  writeFileSync(path.join(consumer, "wire.ts"), declarations.join("\n"));
  const readme = readFileSync(path.join(packageRoot, "README.md"), "utf8");
  const examples = [...readme.matchAll(/```ts\r?\n([\s\S]*?)```/gu)].map((match) => match[1]);
  if (examples.length === 0) throw new Error("TypeScript README has no executable examples");
  writeFileSync(path.join(consumer, "readme.ts"), examples.join("\n"));
  writeFileSync(path.join(consumer, "tsconfig.json"), JSON.stringify({
    compilerOptions: {
      strict: true,
      noEmit: true,
      module: "NodeNext",
      moduleResolution: "NodeNext",
      target: "ES2022",
      skipLibCheck: false,
    },
    include: ["index.ts", "readme.ts", "wire.ts"],
  }, null, 2));
  run("pnpm", ["add", archivePath], consumer);
  const tsc = path.join(root, "node_modules", "typescript", "bin", "tsc");
  run(process.execPath, [tsc, "--project", path.join(consumer, "tsconfig.json")], root);
  run(process.execPath, ["index.mjs"], consumer);
}

function run(command, args, cwd, env = process.env, capture = false) {
  const usesCommandShim = process.platform === "win32" && ["npm", "pnpm"].includes(command);
  const executable = usesCommandShim ? (process.env.ComSpec ?? "cmd.exe") : command;
  const executableArgs = usesCommandShim ? ["/d", "/s", "/c", command, ...args] : args;
  const result = spawnSync(executable, executableArgs, {
    cwd,
    env,
    encoding: "utf8",
    shell: false,
    stdio: capture ? "pipe" : "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}\n${result.stdout ?? ""}\n${result.stderr ?? ""}`);
  }
  return capture ? (result.stdout ?? "").trim() : "";
}
