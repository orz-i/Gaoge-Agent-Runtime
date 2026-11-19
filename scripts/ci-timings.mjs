import { spawnSync } from "node:child_process";
import { appendFileSync, existsSync, mkdirSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";

// Opt-in outside CI. Store labels and results only, never argv or environment.
export function measureCI(label, task, { env = process.env, clock = () => performance.now() } = {}) {
  if (!env.CI_TIMING_DIRECTORY) return task();
  const job = env.GITHUB_JOB ?? "local";
  if (!/^[A-Za-z0-9_-]+$/u.test(job)) throw new Error("invalid timing job name");
  const startedAt = new Date().toISOString();
  const start = clock();
  let exitCode = 0;
  let signal = null;
  try {
    return task();
  } catch (error) {
    exitCode = Number.isInteger(error?.exitCode) && error.exitCode > 0 ? error.exitCode : 1;
    signal = error?.signal ?? null;
    throw error;
  } finally {
    const durationMs = Math.max(0, Math.round(clock() - start));
    const record = { schemaVersion: 1, job, kind: label.kind, name: label.name,
      ...(label.mode ? { mode: label.mode } : {}), startedAt, durationMs, exitCode, signal };
    mkdirSync(env.CI_TIMING_DIRECTORY, { recursive: true });
    appendFileSync(path.join(env.CI_TIMING_DIRECTORY, `${job}.jsonl`), `${JSON.stringify(record)}\n`);
    console.log(`[ci-timing] ${label.kind} ${label.name}${label.mode ? ` (${label.mode})` : ""}: ${(durationMs / 1000).toFixed(3)}s, exit ${exitCode}${signal ? `, ${signal}` : ""}`);
  }
}

export function runCICommand(command, args) {
  const result = spawnSync(command, args, { stdio: "inherit", shell: false });
  if (result.error || result.status !== 0) {
    const error = new Error(result.error ? `could not launch ${command}: ${result.error.message}` : `${command} failed (${result.signal ?? result.status ?? "unknown"})`);
    error.exitCode = result.status > 0 ? result.status : 1;
    error.signal = result.signal;
    throw error;
  }
}

const markdown = (value) => String(value).replaceAll("&", "&amp;").replaceAll("<", "&lt;")
  .replaceAll("|", "\\|").replaceAll("`", "\\`").replace(/[\r\n]/gu, " ");

export function timingSummary(directory) {
  const records = existsSync(directory) ? readdirSync(directory).filter((name) => name.endsWith(".jsonl"))
    .flatMap((name) => readFileSync(path.join(directory, name), "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line))) : [];
  for (const record of records) {
    if (record.schemaVersion !== 1 || !["target", "go-module", "command"].includes(record.kind) ||
        typeof record.name !== "string" || !Number.isFinite(record.durationMs) || record.durationMs < 0 ||
        !Number.isInteger(record.exitCode) || record.exitCode < 0) {
      throw new Error("invalid CI timing record");
    }
  }
  let summary = "## CI timing baseline\n\nInformational wall-clock durations, not performance gates. Nested target/module times overlap; do not add them together.\n\n";
  if (!records.length) return summary + "No measured commands completed (the job may have stopped during setup).\n";
  for (const [heading, rows] of [
    ["Targets and commands", records.filter((record) => record.kind !== "go-module")],
    ["Slowest Go module operations (up to 20)", records.filter((record) => record.kind === "go-module")],
  ]) {
    summary += `### ${heading}\n\n| Check | Seconds | Result |\n| --- | ---: | --- |\n`;
    for (const row of rows.sort((a, b) => b.durationMs - a.durationMs).slice(0, 20)) {
      summary += `| ${markdown(row.name)}${row.mode ? ` / ${markdown(row.mode)}` : ""} | ${(row.durationMs / 1000).toFixed(3)} | ${row.exitCode === 0 ? "PASS" : `FAIL (${row.exitCode}${row.signal ? `, ${markdown(row.signal)}` : ""})`} |\n`;
    }
    summary += "\n";
  }
  return summary;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    if (args.length === 1 && args[0] === "--summary") {
      if (!process.env.CI_TIMING_DIRECTORY) throw new Error("CI_TIMING_DIRECTORY is required");
      const summary = timingSummary(process.env.CI_TIMING_DIRECTORY);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
      else console.log(summary);
    } else if (args[0] === "--run" && args[1] && args[2] === "--" && args[3]) {
      measureCI({ kind: "command", name: args[1] }, () => runCICommand(args[3], args.slice(4)));
    } else {
      throw new Error("usage: ci-timings.mjs --summary | --run <label> -- <command> [args...]");
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = error.exitCode ?? 1;
  }
}
