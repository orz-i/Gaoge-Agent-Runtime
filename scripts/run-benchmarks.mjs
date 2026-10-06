import { mkdirSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const moduleRoot = path.join(root, "go", "agent-runtime");
const smoke = process.argv.includes("--smoke");
const outputFlag = process.argv.find((value) => value.startsWith("--output="));
const outputPath = outputFlag ? path.resolve(root, outputFlag.slice("--output=".length)) : null;

const args = [
  "test",
  "./kernel",
  "./runrelation",
  "-run",
  "^$",
  "-bench",
  "^(BenchmarkRuntime|BenchmarkRunRelation)",
  "-benchmem",
  `-benchtime=${smoke ? "1x" : "1000x"}`,
  `-count=${smoke ? "1" : "5"}`,
];

const result = spawnSync("go", args, {
  cwd: moduleRoot,
  env: process.env,
  encoding: "utf8",
  shell: false,
  maxBuffer: 100 * 1024 * 1024,
});
process.stdout.write(result.stdout ?? "");
process.stderr.write(result.stderr ?? "");
if (result.status !== 0) {
  process.exit(result.status ?? 1);
}

const report = parseBenchmarkOutput(result.stdout ?? "");
if (Object.keys(report.benchmarks).length === 0) {
  throw new Error("no benchmark results were parsed");
}
if (outputPath) {
  mkdirSync(path.dirname(outputPath), { recursive: true });
  writeFileSync(outputPath, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`Wrote benchmark report to ${path.relative(root, outputPath)}`);
}

function parseBenchmarkOutput(output) {
  const environment = {};
  const samples = new Map();
  for (const line of output.split(/\r?\n/u)) {
    const separator = line.indexOf(":");
    if (separator > 0) {
      const key = line.slice(0, separator).trim();
      if (["goos", "goarch", "cpu"].includes(key) && !environment[key]) {
        environment[key] = line.slice(separator + 1).trim();
      }
    }
    const match = line.match(
      /^(Benchmark\S+?)-\d+\s+\d+\s+([\d.]+)\s+ns\/op(?:\s+([\d.]+)\s+B\/op\s+([\d.]+)\s+allocs\/op)?$/u,
    );
    if (!match) continue;
    const [, name, nsPerOp, bytesPerOp, allocsPerOp] = match;
    const current = samples.get(name) ?? [];
    current.push({
      nsPerOp: Number(nsPerOp),
      bytesPerOp: Number(bytesPerOp ?? 0),
      allocsPerOp: Number(allocsPerOp ?? 0),
    });
    samples.set(name, current);
  }

  const benchmarks = {};
  for (const [name, values] of [...samples.entries()].sort(([left], [right]) => left.localeCompare(right))) {
    benchmarks[name] = {
      samples: values.length,
      nsPerOp: median(values.map((value) => value.nsPerOp)),
      bytesPerOp: median(values.map((value) => value.bytesPerOp)),
      allocsPerOp: median(values.map((value) => value.allocsPerOp)),
    };
  }
  return {
    schemaVersion: 1,
    environment,
    thresholds: {
      nsPerOpPercent: 50,
      bytesPerOpPercent: 25,
      allocsPerOpPercent: 10,
    },
    benchmarks,
  };
}

function median(values) {
  const sorted = [...values].sort((left, right) => left - right);
  const middle = Math.floor(sorted.length / 2);
  if (sorted.length % 2 === 1) return sorted[middle];
  return (sorted[middle - 1] + sorted[middle]) / 2;
}
