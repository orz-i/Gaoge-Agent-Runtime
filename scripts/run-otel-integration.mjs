import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const externalServices = process.argv.includes("--external-services");
const compose = ["compose", "-p", "gaoge-agent-runtime-otel", "-f", "docker-compose.test.yml"];

if (externalServices) {
  const endpoint = process.env.TEST_OTEL_HTTP_ENDPOINT?.trim();
  if (!endpoint) throw new Error("TEST_OTEL_HTTP_ENDPOINT is required for --external-services");
  emit({ ...process.env, TEST_OTEL_HTTP_ENDPOINT: endpoint });
} else {
  try {
    run("docker", [...compose, "up", "-d", "otel-collector"], root);
    run("sleep", ["2"], root);
    emit({ ...process.env, TEST_OTEL_HTTP_ENDPOINT: "127.0.0.1:54318" });
    run("sleep", ["1"], root);
    const logs = runCapture("docker", [...compose, "logs", "--no-color", "otel-collector"], root);
    for (const marker of [
      "agent.runtime.run",
      "agent.runtime.model",
      "agent.runtime.operation.count",
      "agent.runtime.model.token.usage",
    ]) {
      if (!logs.includes(marker)) {
        throw new Error(`OpenTelemetry Collector output missing ${marker}`);
      }
    }
    console.log("OpenTelemetry Collector E2E trace/metric gate passed");
  } finally {
    run("docker", [...compose, "down", "-v", "--remove-orphans"], root, true);
  }
}

function emit(env) {
  run("go", ["run", "."], path.join(root, "integration/otel-e2e"), false, { ...env, GOWORK: "off" });
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

function runCapture(command, args, cwd, env = process.env) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    shell: false,
    maxBuffer: 100 * 1024 * 1024,
  });
  if (result.status !== 0) {
    process.stdout.write(result.stdout ?? "");
    process.stderr.write(result.stderr ?? "");
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}`);
  }
  return `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
}
