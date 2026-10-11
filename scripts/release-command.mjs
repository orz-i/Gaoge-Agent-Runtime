import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

// npm and pnpm are commonly .cmd launchers on Windows. Never pass release
// paths to cmd.exe: it interprets &, %, ^, and other characters in arguments.
export function resolveReleaseCommand(command, args, {
  platform = process.platform,
  nodeExecutable = process.execPath,
  env = process.env,
  exists = existsSync,
} = {}) {
  if (platform !== "win32" || (command !== "npm" && command !== "pnpm")) {
    return { executable: command, args };
  }

  const pathKey = Object.keys(env).find((key) => key.toUpperCase() === "PATH");
  const pathDirectories = pathKey ? env[pathKey].split(path.win32.delimiter).filter(Boolean) : [];
  const additionalDirectories = env.PNPM_HOME
    ? [env.PNPM_HOME, path.join(env.PNPM_HOME, "bin")]
    : [];

  // pnpm/action-setup may install a native pnpm.exe. It accepts argv directly,
  // including paths with spaces or shell metacharacters, without cmd.exe.
  if (command === "pnpm") {
    for (const directory of [...pathDirectories, ...additionalDirectories]) {
      const executable = path.join(directory, "pnpm.exe");
      if (exists(executable)) return { executable, args };
    }
  }

  // Otherwise run the JavaScript CLI with Node directly. Both npm's global
  // layout and pnpm's node_modules/.bin shim layout are supported.
  const directories = [path.dirname(nodeExecutable), ...pathDirectories, ...additionalDirectories];
  const cliNames = command === "npm" ? ["npm-cli.js"] : ["pnpm.mjs", "pnpm.cjs"];
  for (const directory of directories) {
    for (const cliName of cliNames) {
      for (const cliPath of [
        path.join(directory, "node_modules", command, "bin", cliName),
        path.join(directory, "..", command, "bin", cliName),
      ]) {
        if (exists(cliPath)) {
          return { executable: nodeExecutable, args: [cliPath, ...args] };
        }
      }
    }
  }

  throw new Error("Cannot locate a shell-free " + command + " CLI on Windows; install Node.js and pnpm with their CLI files on PATH");
}

export function run(command, args, cwd, env = process.env, capture = false) {
  const { executable, args: executableArgs } = resolveReleaseCommand(command, args, { env });
  const result = spawnSync(executable, executableArgs, {
    cwd,
    env,
    encoding: "utf8",
    shell: false,
    stdio: capture ? "pipe" : "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (result.error) {
    throw new Error(command + " failed to start: " + result.error.message, { cause: result.error });
  }
  if (result.status !== 0) {
    throw new Error(command + " " + args.join(" ") + " failed with exit code " + (result.status ?? 1)
      + "\n" + (result.stdout ?? "") + "\n" + (result.stderr ?? ""));
  }
  return capture ? (result.stdout ?? "").trim() : "";
}
