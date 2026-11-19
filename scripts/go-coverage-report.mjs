import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { summarizeGoCoverage } from "./go-coverage-summary.mjs";

export function writeGoCoverageReport(profiles, directory, minimumValue = process.env.GO_COVERAGE_MIN ?? "70") {
  const { coveredStatements, totalStatements } = summarizeGoCoverage(profiles);
  const percent = totalStatements === 0 ? 100 : (coveredStatements / totalStatements) * 100;
  const minimum = Number(minimumValue);
  if (!Number.isFinite(minimum) || minimum < 0 || minimum > 100) {
    throw new Error(`invalid GO_COVERAGE_MIN: ${minimumValue}`);
  }
  const summary = { coveredStatements, totalStatements, percent: Number(percent.toFixed(2)), minimum };
  mkdirSync(directory, { recursive: true });
  writeFileSync(path.join(directory, "summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
  console.log(`Go statement coverage: ${percent.toFixed(2)}% (minimum ${minimum.toFixed(2)}%)`);
  if (percent < minimum) {
    throw new Error(`Go statement coverage ${percent.toFixed(2)}% is below ${minimum.toFixed(2)}%`);
  }
  return summary;
}
