import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const workflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");

test("Beta releases never publish to the npm registry or request publication credentials", () => {
  assert.doesNotMatch(workflow, /\b(?:npm|pnpm|yarn)\s+(?:npm\s+)?publish\b/u);
  assert.doesNotMatch(workflow, /NPM_TOKEN|NODE_AUTH_TOKEN|registry-url|id-token\s*:\s*write/u);
});

test("Beta releases retain immutable tags, reviewed notes, and a TypeScript archive", () => {
  assert.match(workflow, /v\*\.\*\.\*-beta\.\*/u);
  assert.match(workflow, /npm pack --pack-destination artifacts \.\/ts\/agent-runtime-client/u);
  assert.match(workflow, /gh release create "\$RELEASE_TAG" artifacts\/\*\.tgz --prerelease --verify-tag/u);
  assert.match(workflow, /--notes-file "docs\/releases\/\$RELEASE_TAG\.md"/u);
});

test("Tag verification and the full Beta gate precede artifact publication", () => {
  const verify = workflow.indexOf("- name: Verify tag set");
  const gate = workflow.indexOf("make check && make integration-test");
  const pack = workflow.indexOf("- name: Pack TypeScript client");
  const release = workflow.indexOf("- name: Create GitHub prerelease");
  assert.ok(verify >= 0 && verify < gate && gate < pack && pack < release);
});
