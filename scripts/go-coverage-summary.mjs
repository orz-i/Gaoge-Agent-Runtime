// Go -coverpkg profiles repeat source blocks across test binaries. Count each
// complete source/range identity once, retaining every zero-coverage block.
// This module performs no I/O and does not choose or relax a coverage threshold.
export function summarizeGoCoverage(profiles) {
  if (!Array.isArray(profiles) || profiles.length === 0) {
    throw new Error("Go coverage requires at least one profile");
  }
  const blocks = new Map();
  let mode;
  for (const { name, contents } of profiles) {
    if (typeof name !== "string" || typeof contents !== "string") {
      throw new Error("invalid Go coverage profile input");
    }
    const lines = contents.replaceAll("\r\n", "\n").split("\n");
    const header = /^mode: (set|count|atomic)$/.exec(lines.shift());
    if (!header) throw new Error(`${name}: invalid Go coverage mode header`);
    if (mode !== undefined && mode !== header[1]) {
      throw new Error(`${name}: mixed Go coverage modes ${mode}/${header[1]}`);
    }
    mode = header[1];
    // A trailing newline is conventional; interior empty lines are malformed.
    if (lines.at(-1) === "") lines.pop();
    for (const [index, line] of lines.entries()) {
      const location = `${name}:${index + 2}`;
      const match = /^(\S+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$/.exec(line);
      if (!match) throw new Error(`${location}: malformed Go coverage block`);
      const [, source, startLine, startColumn, endLine, endColumn, statements, executions] = match;
      const coordinates = [startLine, startColumn, endLine, endColumn].map((value) => {
        const number = Number(value);
        if (!Number.isSafeInteger(number) || number <= 0) {
          throw new Error(`${location}: invalid Go coverage coordinate`);
        }
        return number;
      });
      if (coordinates[2] < coordinates[0] ||
          (coordinates[2] === coordinates[0] && coordinates[3] < coordinates[1])) {
        throw new Error(`${location}: reversed Go coverage range`);
      }
      const statementCount = Number(statements);
      if (!Number.isSafeInteger(statementCount) || statementCount < 0) {
        throw new Error(`${location}: unsafe Go coverage statement count`);
      }
      // Counts need only establish positive execution. BigInt avoids rounding
      // large atomic counters; never sum them or confuse them with statements.
      const executionCount = BigInt(executions);
      if (mode === "set" && executionCount > 1n) {
        throw new Error(`${location}: invalid set-mode execution count`);
      }
      const key = JSON.stringify([source, ...coordinates]);
      const previous = blocks.get(key);
      if (previous && previous.statements !== statementCount) {
        throw new Error(`${location}: conflicting statement counts for ${source}:${coordinates.join(",")}`);
      }
      blocks.set(key, {
        statements: statementCount,
        covered: (previous?.covered ?? false) || executionCount > 0n,
      });
    }
  }
  let coveredStatements = 0;
  let totalStatements = 0;
  for (const block of blocks.values()) {
    totalStatements += block.statements;
    if (block.covered) coveredStatements += block.statements;
    if (!Number.isSafeInteger(totalStatements) || !Number.isSafeInteger(coveredStatements)) {
      throw new Error("Go coverage statement total exceeds safe integer precision");
    }
  }
  return { coveredStatements, totalStatements };
}
