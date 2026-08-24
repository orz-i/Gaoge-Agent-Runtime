import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const externalServices = process.argv.includes("--external-services");
const compose = ["compose", "-p", "gaoge-agent-runtime-beta", "-f", "docker-compose.test.yml"];

if (externalServices) {
  requireEnvironment();
  runSuites(process.env);
} else {
  try {
    run("docker", [...compose, "up", "-d", "--wait", "--wait-timeout", "120"], root);
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
  run("go", ["test", "./...", "-run", "^TestRealPostgres", "-count=1", "-v"], path.join(root, "go/agent-runtime-postgres"), false, env);
  run("go", ["test", "./...", "-run", "^TestRealPostgres", "-count=1", "-v"], path.join(root, "go/agent-runtime-harness-postgres"), false, env);
  run("go", ["test", "./...", "-run", "^TestRealRedis", "-count=1", "-v"], path.join(root, "go/agent-runtime-redis"), false, env);
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
