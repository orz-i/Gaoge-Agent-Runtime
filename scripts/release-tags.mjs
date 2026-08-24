import { readFileSync } from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const version = readFileSync(path.join(root, "VERSION"), "utf8").trim();
const requestedVersion = argument("--version") ?? version;
const create = process.argv.includes("--create");
if (requestedVersion !== version) throw new Error(`requested ${requestedVersion}, repository VERSION is ${version}`);
if (!/^\d+\.\d+\.\d+-beta\.\d+$/u.test(version)) throw new Error(`VERSION is not a Beta SemVer: ${version}`);

const moduleDirectories = [
  "go/agent-runtime",
  "go/agent-runtime-harness",
  "go/agent-runtime-harness-postgres",
  "go/agent-runtime-mcp",
  "go/agent-runtime-a2a",
  "go/agent-runtime-postgres",
  "go/agent-runtime-redis",
  "go/agent-runtime-http",
];
const tags = [`v${version}`, ...moduleDirectories.map((directory) => `${directory}/v${version}`)];

if (create) {
  if (git(["status", "--porcelain", "--untracked-files=all"])) throw new Error("release tags require a clean worktree");
  for (const tag of tags) {
    try {
      git(["rev-parse", "--verify", `refs/tags/${tag}`]);
      throw new Error(`tag already exists: ${tag}`);
    } catch (error) {
      if (error.message?.startsWith("tag already exists")) throw error;
    }
  }
  for (const tag of tags) git(["tag", "-a", tag, "-m", `Agent Runtime ${version}`]);
  console.log(`created ${tags.length} annotated tags`);
} else {
  console.log("Release tag set:");
  for (const tag of tags) console.log(tag);
}
console.log(`Push atomically with:\ngit push --atomic origin ${tags.join(" ")}`);

function argument(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : null;
}

function git(args) {
  return execFileSync("git", args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
}
