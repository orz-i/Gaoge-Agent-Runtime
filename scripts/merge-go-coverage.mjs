import { copyFileSync, mkdirSync, readFileSync, readdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { coverageProfileName, modules, selectModules, shards, validateLayout } from "./go-workspace-layout.mjs";
import { writeGoCoverageReport } from "./go-coverage-report.mjs";

export function mergeGoCoverage(source, output, { commit = process.env.GITHUB_SHA ?? null, minimum } = {}) {
  validateLayout();
  const expectedDirectories = Object.keys(shards).map((shard) => `go-coverage-${shard}`).sort();
  const entries = readdirSync(source, { withFileTypes: true });
  if (entries.some((entry) => !entry.isDirectory()) ||
      JSON.stringify(entries.map((entry) => entry.name).sort()) !== JSON.stringify(expectedDirectories)) {
    throw new Error("coverage requires exactly the three current shard artifacts");
  }
  const profiles = [];
  for (const shard of Object.keys(shards)) {
    const directory = path.join(source, `go-coverage-${shard}`);
    const selected = selectModules("coverage", [`--shard=${shard}`]).selected;
    const files = readdirSync(directory, { withFileTypes: true });
    const expectedFiles = ["manifest.json", ...selected.map(coverageProfileName)].sort();
    if (files.some((entry) => !entry.isFile()) ||
        JSON.stringify(files.map((entry) => entry.name).sort()) !== JSON.stringify(expectedFiles)) {
      throw new Error(`${shard}: missing, duplicate, or unexpected coverage files`);
    }
    const manifest = JSON.parse(readFileSync(path.join(directory, "manifest.json"), "utf8"));
    if (manifest.schemaVersion !== 1 || manifest.shard !== shard || manifest.commit !== commit ||
        JSON.stringify(manifest.modules) !== JSON.stringify(selected)) {
      throw new Error(`${shard}: coverage manifest does not match the current commit and complete module set`);
    }
    for (const module of selected) {
      const name = coverageProfileName(module);
      const contents = readFileSync(path.join(directory, name), "utf8");
      if (!/^mode: atomic\r?\n/u.test(contents)) throw new Error(`${name}: expected atomic coverage`);
      profiles.push({ name, contents, source: path.join(directory, name) });
    }
  }
  if (profiles.length !== modules.length || new Set(profiles.map((profile) => profile.name)).size !== modules.length) {
    throw new Error("coverage must contain one profile for every workspace module");
  }
  // Only task-owned report output is replaced; source artifacts remain intact.
  rmSync(output, { recursive: true, force: true });
  mkdirSync(output, { recursive: true });
  for (const profile of profiles) copyFileSync(profile.source, path.join(output, profile.name));
  return writeGoCoverageReport(profiles, output, minimum);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 3) throw new Error("usage: node scripts/merge-go-coverage.mjs <downloaded-shard-artifacts>");
    mergeGoCoverage(path.resolve(process.argv[2]), path.resolve("coverage/go"));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
