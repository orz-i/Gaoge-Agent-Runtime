import { cpSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "./release-command.mjs";
import { transportModule, transportDirectory, transportDependencyViolations, transportMetadataViolations } from "./provider-transport-policy.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const argument = process.argv[2];
const published = argument === "--published";
const revision = argument?.startsWith("--revision=") ? argument.slice("--revision=".length) : null;
if (process.argv.length > 3 || (argument && !published && !/^[a-f0-9]{40}$/u.test(revision ?? ""))) {
  throw new Error("usage: node scripts/check-provider-transport.mjs [--published|--revision=<full SHA>]");
}
const violations = transportMetadataViolations(root);
if (violations.length) throw new Error(violations.join("\n"));
const version = readFileSync(path.join(root, transportDirectory, "VERSION"), "utf8").trim();
const temporary = mkdtempSync(path.join(tmpdir(), "gaoge-provider-transport-"));
const env = { ...process.env, GOWORK: "off", GOFLAGS: "" };

function assertIndependent(cwd) {
  const packages = run("go", ["list", "-deps", "-test", "./..."], cwd, env, true).trim().split("\n");
  const modules = run("go", ["list", "-m", "all"], cwd, env, true).trim().split("\n");
  const errors = transportDependencyViolations(packages, modules);
  if (errors.length) throw new Error(errors.join("\n"));
}

try {
  let replacement = "";
  if (!published && !revision) {
    // Copy only this module out of the SDK checkout. No workspace or Runtime
    // source replacements can accidentally satisfy its dependencies.
    const isolated = path.join(temporary, "module");
    cpSync(path.join(root, transportDirectory), isolated, { recursive: true });
    const before = readFileSync(path.join(isolated, "go.mod"), "utf8");
    const beforeSum = readFileSync(path.join(isolated, "go.sum"), "utf8");
    run("go", ["mod", "tidy"], isolated, env);
    if (before !== readFileSync(path.join(isolated, "go.mod"), "utf8") || beforeSum !== readFileSync(path.join(isolated, "go.sum"), "utf8")) {
      throw new Error("standalone transport module is not tidy");
    }
    run("go", ["test", "./...", "-count=1"], isolated, env);
    run("go", ["vet", "./..."], isolated, env);
    assertIndependent(isolated);
    replacement = `\nreplace ${transportModule} => ${JSON.stringify(isolated.replaceAll("\\", "/"))}\n`;
  }
  const consumer = path.join(temporary, "consumer");
  mkdirSync(consumer);
  writeFileSync(path.join(consumer, "go.mod"), `module example.com/independent-provider-transport-consumer\n\ngo 1.26\n\n${revision ? "" : `require ${transportModule} v${version}\n`}${replacement}`);
  writeFileSync(path.join(consumer, "transport_test.go"), readFileSync(path.join(root, "contracts/consumers/go/provider-transport_test.go")));
  if (revision) run("go", ["get", `${transportModule}@${revision}`], consumer, env);
  run("go", ["mod", "tidy"], consumer, env);
  if ((published || revision) && /^replace(?:\s|\()/mu.test(readFileSync(path.join(consumer, "go.mod"), "utf8"))) {
    throw new Error("remote consumer unexpectedly contains a local replacement");
  }
  run("go", ["test", "./...", "-count=1"], consumer, env);
  assertIndependent(consumer);
  console.log(`Provider Transport independent consumer PASS (${published ? `published v${version}` : revision ? `remote ${revision}; no replaces` : "isolated local source; not a release"})`);
} finally {
  rmSync(temporary, { recursive: true, force: true });
}
