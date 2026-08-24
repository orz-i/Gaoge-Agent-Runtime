import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const tckCommit = "5996b79f9cefa6fc390980e383e358a66fb9e49e";
const tckRepository = "https://github.com/a2aproject/a2a-tck.git";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const explicitTCKDirectory = process.env.A2A_TCK_DIR?.trim();
const tckDirectory = explicitTCKDirectory
  ? path.resolve(explicitTCKDirectory)
  : path.join(tmpdir(), `gaoge-a2a-tck-${tckCommit.slice(0, 12)}`);
const temporary = mkdtempSync(path.join(tmpdir(), "gaoge-a2a-sut-"));
const sutBinary = path.join(temporary, process.platform === "win32" ? "a2a-tck-sut.exe" : "a2a-tck-sut");
let sut;

try {
  prepareTCK();
  run("uv", ["sync", "--frozen"], tckDirectory);
  run("go", ["build", "-o", sutBinary, "./internal/tcksut"], path.join(root, "go", "agent-runtime-a2a"));

  sut = spawn(sutBinary, ["--listen", "127.0.0.1:0"], {
    cwd: path.join(root, "go", "agent-runtime-a2a"),
    shell: false,
    stdio: ["ignore", "pipe", "inherit"],
  });
  const sutURL = await waitForSUT(sut);
  run("uv", ["run", "python", "run_tck.py", "--sut-host", sutURL, "--transport", "http_json"], tckDirectory);

  const reportPath = path.join(tckDirectory, "reports", "compatibility.json");
  const report = JSON.parse(readFileSync(reportPath, "utf8"));
  assertCompatible(report);
  exportEvidence(report, reportPath, sutURL);
  console.log(`Official A2A TCK ${tckCommit} HTTP+JSON gate passed at ${report.summary.overall_compatibility}`);
} finally {
  await stopSUT(sut);
  rmSync(temporary, { recursive: true, force: true });
}

function prepareTCK() {
  if (explicitTCKDirectory) {
    const actual = gitOutput(["rev-parse", "HEAD"], tckDirectory);
    if (actual !== tckCommit) {
      throw new Error(`A2A_TCK_DIR must be pinned to ${tckCommit}; found ${actual}`);
    }
    return;
  }
  if (!existsSync(path.join(tckDirectory, ".git"))) {
    run("git", ["clone", "--filter=blob:none", "--no-tags", tckRepository, tckDirectory], tmpdir());
  }
  run("git", ["fetch", "--depth=1", "origin", tckCommit], tckDirectory);
  run("git", ["checkout", "--detach", tckCommit], tckDirectory);
  const actual = gitOutput(["rev-parse", "HEAD"], tckDirectory);
  if (actual !== tckCommit) throw new Error(`failed to pin official A2A TCK to ${tckCommit}`);
}

function waitForSUT(child) {
  return new Promise((resolve, reject) => {
    let output = "";
    const timer = setTimeout(() => reject(new Error("A2A TCK SUT did not become ready within 20 seconds")), 20_000);
    child.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`A2A TCK SUT exited before readiness with code ${code ?? 1}`));
    });
    child.stdout.on("data", (chunk) => {
      output += chunk.toString("utf8");
      const match = output.match(/A2A_TCK_SUT_READY=(http:\/\/[^\s]+)/u);
      if (!match) return;
      clearTimeout(timer);
      resolve(match[1]);
    });
  });
}

async function stopSUT(child) {
  if (!child || child.exitCode !== null) return;
  child.kill();
  const exited = await Promise.race([
    once(child, "exit").then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), 3_000)),
  ]);
  if (exited || child.exitCode !== null) return;
  if (process.platform === "win32") {
    run("taskkill", ["/pid", String(child.pid), "/t", "/f"], root, true);
  } else {
    child.kill("SIGKILL");
  }
}

function assertCompatible(report) {
  const httpJSON = report.per_transport?.http_json;
  const agentCard = report.per_transport?.agent_card;
  const interfaces = report.agent_card?.supportedInterfaces ?? [];
  if (report.summary?.overall_compatibility !== "100.0%" || !httpJSON || httpJSON.failed !== 0 ||
      !agentCard || agentCard.failed !== 0 || interfaces.length !== 1 ||
      interfaces[0].protocolBinding !== "HTTP+JSON" || interfaces[0].protocolVersion !== "1.0") {
    throw new Error("official A2A TCK report did not satisfy the Beta.2 HTTP+JSON contract");
  }
}

function exportEvidence(report, reportPath, sutURL) {
  const reportDirectory = process.env.A2A_TCK_REPORT_DIR?.trim();
  if (!reportDirectory) return;
  const destination = path.resolve(reportDirectory);
  mkdirSync(destination, { recursive: true });
  for (const name of ["compatibility.json", "compatibility.html", "junitreport.xml", "tck_report.html"]) {
    const source = path.join(path.dirname(reportPath), name);
    if (existsSync(source)) copyFileSync(source, path.join(destination, name));
  }
  const evidence = {
    schemaVersion: 1,
    sdkVersion: readFileSync(path.join(root, "VERSION"), "utf8").trim(),
    sdkCommit: gitOutput(["rev-parse", "HEAD"], root),
    tckRepository,
    tckCommit,
    transport: "http_json",
    protocolVersion: "1.0",
    sutURL,
    overallCompatibility: report.summary.overall_compatibility,
    agentCard: report.per_transport.agent_card,
    httpJSON: report.per_transport.http_json,
    recordedAt: new Date().toISOString(),
  };
  writeFileSync(path.join(destination, "gaoge-a2a-tck-evidence.json"), `${JSON.stringify(evidence, null, 2)}\n`);
}

function gitOutput(args, cwd) {
  return run("git", args, cwd, false, true);
}

function run(command, args, cwd, allowFailure = false, capture = false) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    shell: false,
    stdio: capture ? "pipe" : "inherit",
    maxBuffer: 100 * 1024 * 1024,
  });
  if (!allowFailure && result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit code ${result.status ?? 1}\n${result.stderr ?? ""}`);
  }
  return capture ? (result.stdout ?? "").trim() : "";
}
