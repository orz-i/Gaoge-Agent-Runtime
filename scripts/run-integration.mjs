import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const externalServices = args.includes("--external-services");
const suite = args.find((arg) => arg.startsWith("--suite="))?.slice("--suite=".length);
const suites = {
  kernel: ["go/agent-runtime-postgres", "^TestRealPostgres", false],
  harness: ["go/agent-runtime-harness-postgres", "^TestRealPostgres", false],
  redis: ["go/agent-runtime-redis", "^TestRealRedis", false],
  continuation: ["integration/continuation-e2e", "^(TestRealPostgres|TestPR151)", true],
};
if (new Set(args).size !== args.length || args.filter((arg) => arg.startsWith("--suite=")).length > 1 ||
    args.some((arg) => arg !== "--external-services" && !arg.startsWith("--suite=")) ||
    (suite !== undefined && !Object.hasOwn(suites, suite))) throw new Error("unknown integration suite or argument");
const compose = ["compose", "-p", "gaoge-agent-runtime-beta", "-f", "docker-compose.test.yml"];

if (externalServices) {
  requireEnvironment();
  runSuites(process.env);
} else {
  try {
    run("docker", [...compose, "up", "-d", "--wait", "--wait-timeout", "120", "postgres", "redis"], root);
    runSuites({
      ...process.env,
      TEST_POSTGRES_DSN: "postgres://agent_runtime:agent_runtime@127.0.0.1:55432/agent_runtime?sslmode=disable",
      TEST_REDIS_ADDR: "127.0.0.1:56379",
    });
  } finally {
    run("docker", [...compose, "down", "-v", "--remove-orphans"], root, true);
  }
}

function requireEnvironment() {
  for (const name of ["TEST_POSTGRES_DSN", "TEST_REDIS_ADDR"]) {
    if (!process.env[name]?.trim()) throw new Error(`${name} is required for --external-services`);
  }
}

function runSuites(env) {
  for (const [directory, pattern, detached] of (suite === undefined ? Object.values(suites) : [suites[suite]])) {
    run("go", ["test", "-race", "./...", "-run", pattern, "-count=1", "-v"], path.join(root, directory), false,
      detached ? { ...env, GOWORK: "off" } : env);
  }
}

function run(command, args, cwd, allowFailure = false, env = process.env) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    shell: false,
    stdio: "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (!allowFailure && result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
  }
}
