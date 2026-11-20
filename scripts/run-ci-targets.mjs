import path from "node:path";
import { fileURLToPath } from "node:url";
import { measureCI, runCICommand } from "./ci-timings.mjs";

export function runCITargets(targets, { run = runCICommand, measure = measureCI } = {}) {
  if (!targets.length || new Set(targets).size !== targets.length ||
      targets.some((target) => typeof target !== "string" || !/^[a-z][a-z0-9-]*$/u.test(target))) {
    throw new Error("provide distinct Make target names, not flags or shell commands");
  }
  for (const target of targets) {
    measure({ kind: "target", name: target }, () => run("make", ["--no-print-directory", target]));
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    runCITargets(process.argv.slice(2));
  } catch (error) {
    console.error(error.message);
    process.exitCode = error.exitCode ?? 1;
  }
}
