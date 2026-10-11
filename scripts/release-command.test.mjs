import assert from "node:assert/strict";
import { existsSync, mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import { resolveReleaseCommand, run } from "./release-command.mjs";

function withDirectory(callback) {
  const directory = mkdtempSync(path.join(tmpdir(), "gaoge release & "));
  try {
    return callback(directory);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function makeFile(filename) {
  mkdirSync(path.dirname(filename), { recursive: true });
  writeFileSync(filename, "");
}

test("non-Windows commands keep their original argument vector", () => {
  const args = ["pack", "--pack-destination", "/a dir/with & special % characters"];
  assert.deepEqual(resolveReleaseCommand("npm", args, { platform: "linux" }), {
    executable: "npm", args,
  });
  assert.deepEqual(resolveReleaseCommand("go", args, { platform: "win32" }), {
    executable: "go", args,
  });
});

test("Windows npm runs its JavaScript entrypoint, not cmd.exe", () => withDirectory((directory) => {
  const nodeExecutable = path.join(directory, "Node & scripts", "node.exe");
  const npmCli = path.join(path.dirname(nodeExecutable), "node_modules", "npm", "bin", "npm-cli.js");
  makeFile(npmCli);
  const args = ["pack", "--pack-destination", path.join(directory, "tarballs & % ! ^")];
  assert.deepEqual(resolveReleaseCommand("npm", args, {
    platform: "win32", nodeExecutable, env: { Path: "", ComSpec: "cmd.exe" },
  }), { executable: nodeExecutable, args: [npmCli, ...args] });
}));

test("Windows pnpm can launch the native executable without cmd.exe", () => withDirectory((directory) => {
  const pnpmDir = path.join(directory, "pnpm home", "bin");
  const pnpmExecutable = path.join(pnpmDir, "pnpm.exe");
  makeFile(pnpmExecutable);
  const args = ["add", path.join(directory, "archive & echo injected.tgz")];
  assert.deepEqual(resolveReleaseCommand("pnpm", args, {
    platform: "win32", nodeExecutable: path.join(directory, "node.exe"),
    env: { PATH: pnpmDir, PNPM_HOME: path.dirname(pnpmDir) },
  }), { executable: pnpmExecutable, args });
}));

test("Windows pnpm supports the node_modules/.bin shim layout without a shell", () => withDirectory((directory) => {
  const pnpmBin = path.join(directory, "node_modules", ".bin");
  const pnpmCli = path.join(pnpmBin, "..", "pnpm", "bin", "pnpm.mjs");
  makeFile(pnpmCli);
  const nodeExecutable = path.join(directory, "node.exe");
  const args = ["add", path.join(directory, "package with spaces & ^ %.tgz")];
  assert.deepEqual(resolveReleaseCommand("pnpm", args, {
    platform: "win32", nodeExecutable, env: { PATH: pnpmBin, PNPM_HOME: pnpmBin },
  }), { executable: nodeExecutable, args: [pnpmCli, ...args] });
}));

test("missing Windows CLI fails closed instead of falling back to cmd.exe", () => withDirectory((directory) => {
  for (const command of ["npm", "pnpm"]) {
    assert.throws(() => resolveReleaseCommand(command, ["test"], {
      platform: "win32", nodeExecutable: path.join(directory, "node.exe"),
      env: { PATH: "", ComSpec: "cmd.exe" },
    }), /Cannot locate a shell-free/u);
  }
}));

test("the runner preserves shell metacharacters as literal argv values", () => withDirectory((directory) => {
  const marker = path.join(directory, "injected");
  const suspicious = "literal & $(touch " + marker + ") % ! ^ with spaces";
  const actual = run(process.execPath, ["-e", "process.stdout.write(process.argv[1])", suspicious],
    directory, process.env, true);
  assert.equal(actual, suspicious);
  assert.equal(existsSync(marker), false);
}));

test("the runner reports executable launch failures instead of a generic exit code", () => withDirectory((directory) => {
  assert.throws(() => run(path.join(directory, "missing-executable"), [], directory),
    /failed to start/u);
}));

test("Windows launches installed npm and pnpm without a shell", { skip: process.platform !== "win32" }, () => withDirectory((directory) => {
  assert.match(run("npm", ["--version"], directory, process.env, true), /^\d+\.\d+\.\d+$/u);
  assert.match(run("pnpm", ["--version"], directory, process.env, true), /^\d+\.\d+\.\d+$/u);
}));
