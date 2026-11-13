import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { summarizeGoCoverage } from "./go-coverage-summary.mjs";

const profile = (body, name = "test.out", mode = "atomic") => ({ name, contents: `mode: ${mode}\n${body}` });
const block = "example.org/module/pkg/file.go:1.1,3.2";

test("unions duplicate blocks within and across test binaries without losing zeros", () => {
  const first = profile(`${block} 4 0\n${block} 4 2\nexample.org/module/pkg/zero.go:1.1,2.1 7 0\n`);
  const second = profile(`${block} 4 0\n${block} 4 999\n`, "second.out");
  assert.deepEqual(summarizeGoCoverage([first, second]), { coveredStatements: 4, totalStatements: 11 });
  assert.deepEqual(summarizeGoCoverage([second, first]), summarizeGoCoverage([first, second]));
  assert.deepEqual(summarizeGoCoverage([first, first, second]), summarizeGoCoverage([first, second]));
});

test("full source path and every range coordinate form block identity", () => {
  const rows = [
    "module-a/pkg/file.go:1.1,3.2 1 1",
    "module-b/pkg/file.go:1.1,3.2 2 0",
    "module-a/other/file.go:1.1,3.2 3 0",
    "module-a/pkg/file.go:1.2,3.2 4 0",
    "module-a/pkg/file.go:2.1,3.2 5 0",
    "module-a/pkg/file.go:1.1,4.2 6 0",
    "module-a/pkg/file.go:1.1,3.3 7 0",
  ];
  assert.deepEqual(summarizeGoCoverage([profile(rows.join("\n"))]), { coveredStatements: 1, totalStatements: 28 });
});

test("all zero-execution blocks remain in the denominator", () => {
  assert.deepEqual(summarizeGoCoverage([profile(`${block} 8 0\n${block} 8 0\n`)]), { coveredStatements: 0, totalStatements: 8 });
});

test("large execution counters use exact positivity without numeric overflow", () => {
  assert.deepEqual(summarizeGoCoverage([profile(`${block} 3 999999999999999999999999999999999999\n`)]), { coveredStatements: 3, totalStatements: 3 });
});

test("same block statement-count conflicts fail closed, including zero counts", () => {
  assert.throws(() => summarizeGoCoverage([profile(`${block} 0 0\n${block} 2 1\n`)]), /conflicting statement counts/);
  assert.throws(() => summarizeGoCoverage([profile(`${block} 2 1`), profile(`${block} 3 0`)]), /conflicting statement counts/);
});

test("valid Go modes are recognized but mixed modes are rejected", () => {
  for (const mode of ["set", "count", "atomic"]) {
    assert.deepEqual(summarizeGoCoverage([profile(`${block} 2 1`, "one", mode)]), { coveredStatements: 2, totalStatements: 2 });
  }
  assert.throws(() => summarizeGoCoverage([profile(`${block} 2 1`), profile(`${block} 2 1`, "two", "count")]), /mixed Go coverage modes/);
  assert.throws(() => summarizeGoCoverage([profile(`${block} 2 2`, "set", "set")]), /invalid set-mode/);
});

test("missing and malformed headers or rows are not silently discarded", () => {
  for (const contents of ["", `${block} 2 1`, "mode: unknown\n", "mode: atomic extra\n", `mode: atomic\n\n${block} 2 1`, `mode: atomic\n${block} 2 1 extra`]) {
    assert.throws(() => summarizeGoCoverage([{ name: "invalid", contents }]));
  }
  assert.throws(() => summarizeGoCoverage([]), /at least one profile/);
  assert.throws(() => summarizeGoCoverage([{ name: "invalid", contents: null }]), /invalid Go coverage profile/);
});

test("illegal integer syntax and unsafe statement values fail closed", () => {
  for (const value of ["-1", "1.5", "NaN", "Infinity", "1e3", "+1"]) {
    assert.throws(() => summarizeGoCoverage([profile(`${block} ${value} 0`)]));
    assert.throws(() => summarizeGoCoverage([profile(`${block} 1 ${value}`)]));
  }
  assert.throws(() => summarizeGoCoverage([profile(`${block} 9007199254740992 0`)]), /unsafe/);
  assert.throws(() => summarizeGoCoverage([profile(`${block} 9007199254740991 0\nother.go:1.1,2.1 1 0`)]), /total exceeds/);
});

test("invalid, reversed, or unsafe coordinates fail closed", () => {
  for (const range of ["0.1,2.1", "1.0,2.1", "1.1,0.2", "1.1,2.0", "3.1,2.1", "1.3,1.2", "9007199254740992.1,9007199254740993.1"]) {
    assert.throws(() => summarizeGoCoverage([profile(`source.go:${range} 1 1`)]));
  }
});

test("CRLF profiles and mode-only zero-statement profiles retain existing empty semantics", () => {
  assert.deepEqual(summarizeGoCoverage([{ name: "windows", contents: `mode: atomic\r\nC:/src/file.go:1.1,2.1 2 1\r\n` }]), { coveredStatements: 2, totalStatements: 2 });
  assert.deepEqual(summarizeGoCoverage([profile("")]), { coveredStatements: 0, totalStatements: 0 });
});

test("workspace gate instruments all local packages and preserves its 70 percent contract", () => {
  const workspace = readFileSync(new URL("./go-workspace.mjs", import.meta.url), "utf8");
  assert.match(workspace, /"-coverpkg=\.\/\.\.\."/);
  assert.match(workspace, /GO_COVERAGE_MIN \?\? "70"/);
  assert.match(workspace, /if \(percent < minimum\)/);
  assert.match(workspace, /summarizeGoCoverage\(profiles\.map/);
  const makefile = readFileSync(new URL("../Makefile", import.meta.url), "utf8");
  assert.match(makefile, /metadata:\n\tnode --test[^\n]*scripts\/go-coverage-summary\.test\.mjs/);
});
