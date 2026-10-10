import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const externalServices = process.argv.includes("--external-services");
const compose = ["compose", "-p", "gaoge-agent-runtime-otel", "-f", "docker-compose.test.yml"];
const defaultCollectorImage =
  "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.149.0";
const collectorImage = process.env.OTEL_COLLECTOR_IMAGE?.trim() || defaultCollectorImage;
const composeEnv = { ...process.env, OTEL_COLLECTOR_IMAGE: collectorImage };

if (externalServices) {
  const endpoint = process.env.TEST_OTEL_HTTP_ENDPOINT?.trim();
  if (!endpoint) throw new Error("TEST_OTEL_HTTP_ENDPOINT is required for --external-services");
  emit({ ...process.env, TEST_OTEL_HTTP_ENDPOINT: endpoint });
} else {
  ensureCollectorImage();
  try {
    run("docker", [...compose, "up", "-d", "--pull", "never", "otel-collector"], root, { env: composeEnv });
    waitForEndpoint("http://127.0.0.1:54318/v1/traces");
    emit({ ...process.env, TEST_OTEL_HTTP_ENDPOINT: "127.0.0.1:54318" });
    waitForCollectorMarkers([
      "agent.runtime.run",
      "agent.runtime.model",
      "agent.runtime.operation.count",
      "agent.runtime.model.token.usage",
    ]);
    console.log("OpenTelemetry Collector E2E trace/metric gate passed");
  } finally {
    run("docker", [...compose, "down", "-v", "--remove-orphans"], root, {
      allowFailure: true,
      env: composeEnv,
    });
  }
}

function ensureCollectorImage() {
  if (commandSucceeded("docker", ["image", "inspect", collectorImage], root, composeEnv)) {
    printCollectorDigest();
    return;
  }
  const configuredAttempts = Number.parseInt(process.env.OTEL_COLLECTOR_PULL_ATTEMPTS ?? "3", 10);
  const attempts = Number.isFinite(configuredAttempts) ? Math.min(Math.max(configuredAttempts, 1), 5) : 3;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    console.log(`Pulling OpenTelemetry Collector image (attempt ${attempt}/${attempts}): ${collectorImage}`);
    const result = spawnSync("docker", ["pull", collectorImage], {
      cwd: root,
      env: composeEnv,
      encoding: "utf8",
      shell: false,
      stdio: "inherit",
      timeout: 180_000,
      maxBuffer: 100 * 1024 * 1024,
    });
    if (result.status === 0) {
      printCollectorDigest();
      return;
    }
    if (attempt < attempts) run("sleep", [String(attempt * 3)], root);
  }
  throw new Error(
    `unable to pull OpenTelemetry Collector image after ${attempts} attempt(s): ${collectorImage}. ` +
      "Preload the image or set OTEL_COLLECTOR_IMAGE to an accessible mirror/digest.",
  );
}

function printCollectorDigest() {
  const resolved = runCapture(
    "docker",
    ["image", "inspect", "--format", "{{join .RepoDigests \",\"}}", collectorImage],
    root,
    composeEnv,
  ).trim();
  console.log(`OpenTelemetry Collector image ready: ${collectorImage}${resolved ? ` -> ${resolved}` : ""}`);
}

function waitForEndpoint(url) {
  const nullDevice = process.platform === "win32" ? "NUL" : "/dev/null";
  for (let attempt = 1; attempt <= 30; attempt++) {
    if (
      commandSucceeded(
        "curl",
        ["--silent", "--show-error", "--max-time", "2", "--output", nullDevice, url],
        root,
        composeEnv,
      )
    ) {
      return;
    }
    run("sleep", ["1"], root);
  }
  const logs = runCapture("docker", [...compose, "logs", "--no-color", "otel-collector"], root, composeEnv);
  throw new Error(`OpenTelemetry Collector OTLP/HTTP endpoint did not become ready.\n${logs}`);
}

function waitForCollectorMarkers(markers) {
  for (let attempt = 1; attempt <= 20; attempt++) {
    const logs = runCapture("docker", [...compose, "logs", "--no-color", "otel-collector"], root, composeEnv);
    if (markers.every((marker) => logs.includes(marker))) return;
    run("sleep", ["1"], root);
  }
  const logs = runCapture("docker", [...compose, "logs", "--no-color", "otel-collector"], root, composeEnv);
  const missing = markers.filter((marker) => !logs.includes(marker));
  throw new Error(`OpenTelemetry Collector output missing: ${missing.join(", ")}\n${logs}`);
}

function emit(env) {
  run("go", ["run", "."], path.join(root, "integration/otel-e2e"), {
    env: { ...env, GOWORK: "off" },
  });
}

function commandSucceeded(command, args, cwd, env) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    shell: false,
    stdio: "ignore",
    timeout: 10_000,
  });
  return result.status === 0;
}

function run(command, args, cwd, options = {}) {
  const result = spawnSync(command, args, {
    cwd,
    env: options.env ?? process.env,
    encoding: "utf8",
    shell: false,
    stdio: "inherit",
    timeout: options.timeoutMs,
    maxBuffer: 100 * 1024 * 1024,
  });
  if (!options.allowFailure && result.status !== 0) {
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
