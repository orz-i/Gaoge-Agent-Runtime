import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const [baselineArg, candidateArg] = process.argv.slice(2);
if (!baselineArg || !candidateArg) {
  throw new Error("usage: node scripts/compare-benchmarks.mjs <baseline.json> <candidate.json>");
}

const baseline = readReport(baselineArg);
const candidate = readReport(candidateArg);
assertCompatibleEnvironment(baseline.environment, candidate.environment);

const regressions = [];
for (const [name, expected] of Object.entries(baseline.benchmarks)) {
  const observed = candidate.benchmarks[name];
  if (!observed) {
    regressions.push(`${name}: missing from candidate report`);
    continue;
  }
  compareMetric(name, "nsPerOp", expected.nsPerOp, observed.nsPerOp, baseline.thresholds.nsPerOpPercent);
  compareMetric(
    name,
    "bytesPerOp",
    expected.bytesPerOp,
    observed.bytesPerOp,
    baseline.thresholds.bytesPerOpPercent,
  );
  compareMetric(
    name,
    "allocsPerOp",
    expected.allocsPerOp,
    observed.allocsPerOp,
    baseline.thresholds.allocsPerOpPercent,
  );
}

if (regressions.length > 0) {
  for (const regression of regressions) console.error(`REGRESSION ${regression}`);
  process.exit(1);
}
console.log(`Benchmark comparison passed for ${Object.keys(baseline.benchmarks).length} benchmarks.`);

function readReport(relativePath) {
  const report = JSON.parse(readFileSync(path.resolve(root, relativePath), "utf8"));
  if (
    report.schemaVersion !== 1 ||
    !report.environment ||
    !report.thresholds ||
    !report.benchmarks ||
    typeof report.benchmarks !== "object"
  ) {
    throw new Error(`invalid benchmark report: ${relativePath}`);
  }
  return report;
}

function assertCompatibleEnvironment(expected, observed) {
  for (const key of ["goos", "goarch", "cpu"]) {
    if (expected[key] !== observed[key]) {
      throw new Error(
        `benchmark environment mismatch for ${key}: baseline=${expected[key] ?? ""} candidate=${observed[key] ?? ""}`,
      );
    }
  }
}

function compareMetric(name, metric, expected, observed, thresholdPercent) {
  if (![expected, observed, thresholdPercent].every(Number.isFinite) || thresholdPercent < 0) {
    regressions.push(`${name}: invalid ${metric} comparison data`);
    return;
  }
  const limit = expected === 0 ? 0 : expected * (1 + thresholdPercent / 100);
  if (observed > limit) {
    regressions.push(
      `${name}: ${metric} ${observed} exceeds baseline ${expected} + ${thresholdPercent}% (${limit.toFixed(2)})`,
    );
  }
}
