import assert from "node:assert/strict";
import test from "node:test";
import { runInNewContext } from "node:vm";
import picomatch from "picomatch";

// Check the installed resolver, not a separately imported test dependency.
// VM timeouts prevent a regressed regex from hanging CI's event loop.
test("picomatch rejects repeated-extglob ReDoS inputs without backtracking", () => {
  const input = `${"a".repeat(60)}!`;
  for (const glob of ["+(*a)", "*(*a)", "+(*)x", "+(*|a)"]) {
    const matcher = picomatch.makeRe(glob);
    assert.equal(
      runInNewContext("matcher.test(input)", { matcher, input }, { timeout: 1000 }),
      false,
      `unsafe repeated extglob: ${glob}`,
    );
  }
});

test("patched matcher preserves ordinary glob behavior", () => {
  assert.equal(picomatch.isMatch("agent.test.ts", "**/*.test.ts"), true);
  assert.equal(picomatch.isMatch("src/agent.go", "**/*.test.ts"), false);
  assert.equal(picomatch.isMatch("file.ts", "file.@(js|ts)"), true);
  assert.equal(picomatch.isMatch("hello", "+(*)"), true);
});
