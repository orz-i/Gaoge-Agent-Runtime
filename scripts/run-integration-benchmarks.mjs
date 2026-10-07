import { mkdirSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const smoke = process.argv.includes("--smoke");
const externalServices = process.argv.includes("--external-services");
const outputFlag = process.argv.find((value) => value.startsWith("--output="));
const outputPath = outputFlag ? path.resolve(root, outputFlag.slice("--output=".length)) : null;
const compose = ["compose", "-p", "gaoge-agent-runtime-benchmark", "-f", "docker-compose.test.yml"];
const benchtime = smoke ? "1x" : "25x";
const count = smoke ? "1" : "3";

let env = process.env;
let services = {
  postgres: process.env.TEST_POSTGRES_VERSION?.trim() || "external",
  redis: process.env.TEST_REDIS_VERSION?.trim() || "external",
};

if (externalServices) {
  requireEnvironment();
  runBenchmarks();
} else {
  try {
    run("docker", [...compose, "up", "-d", "--wait", "--wait-timeout", "120", "postgres", "redis"], root);
    env = {
      ...process.env,
      TEST_POSTGRES_DSN: "postgres://agent_runtime:agent_runtime@127.0.0.1:55432/agent_runtime?sslmode=disable",
      TEST_REDIS_ADDR: "127.0.0.1:56379",
    };
    services = { postgres: "postgres:16-alpine", redis: "redis:8-alpine" };
    runBenchmarks();
  } finally {
    run("docker", [...compose, "down", "-v", "--remove-orphans"], root, true);
  }
}

function requireEnvironment() {
  for (const name of ["TEST_POSTGRES_DSN", "TEST_REDIS_ADDR"]) {
    if (!process.env[name]?.trim()) throw new Error(`${name} is required for --external-services`);
  }
}

function runBenchmarks() {
  const suites = [
    {
      cwd: path.join(root, "go", "agent-runtime-postgres"),
      benchmark: "^BenchmarkRealPostgres",
    },
    {
      cwd: path.join(root, "go", "agent-runtime-redis"),
      benchmark: "^BenchmarkRealRedis",
    },
  ];
  let output = "";
  for (const suite of suites) {
    const result = spawnSync("go", [
      "test", ".", "-run", "^$", "-bench", suite.benchmark, "-benchmem",
      `-benchtime=${benchtime}`, `-count=${count}`,
    ], {
      cwd: suite.cwd,
      env,
      encoding: "utf8",
      shell: false,
      maxBuffer: 100 * 1024 * 1024,
    });
    process.stdout.write(result.stdout ?? "");
    process.stderr.write(result.stderr ?? "");
    if (result.status !== 0) process.exit(result.status ?? 1);
    output += `${result.stdout ?? ""}\n`;
  }
  const report = parseBenchmarkOutput(output);
  report.services = services;
  if (Object.keys(report.benchmarks).length === 0) {
    throw new Error("no integration benchmark results were parsed");
  }
  if (outputPath) {
    mkdirSync(path.dirname(outputPath), { recursive: true });
    writeFileSync(outputPath, `${JSON.stringify(report, null, 2)}\n`);
    console.log(`Wrote integration benchmark report to ${path.relative(root, outputPath)}`);
  }
}

function parseBenchmarkOutput(rawOutput) {
  const environment = {};
  const samples = new Map();
  for (const line of rawOutput.split(/\r?\n/u)) {
    const separator = line.indexOf(":");
    if (separator > 0) {
      const key = line.slice(0, separator).trim();
      if (["goos", "goarch", "cpu"].includes(key)) {
        const value = line.slice(separator + 1).trim();
        if (environment[key] && environment[key] !== value) {
          throw new Error(`benchmark environment mismatch inside report for ${key}: ${environment[key]} != ${value}`);
        }
        environment[key] = value;
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
      nsPerOpPercent: 75,
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

function run(command, args, cwd, allowFailure = false) {
  const result = spawnSync(command, args, {
    cwd,
    env: process.env,
    encoding: "utf8",
    shell: false,
    stdio: "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (!allowFailure && result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
  }
}
