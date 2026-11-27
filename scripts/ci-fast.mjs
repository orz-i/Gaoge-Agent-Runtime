import { execFileSync, spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { collectImpact, impactContext } from "./ci-impact.mjs";
import { ownerOf } from "./ci-impact-policy.mjs";
import { modules } from "./go-workspace-layout.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const core = "go/agent-runtime";
const harness = "go/agent-runtime-harness";
const stores = [core + "/memory", core + "-sqlite", core + "-postgres"];
const hostStores = [harness + "-sqlite", harness + "-postgres"];
const covered = new Set([core, harness, ...stores.slice(1), ...hostStores]);
const prefix = "github.com/orz-i/Gaoge-Agent-Runtime/";
const goPath = (module) => "./" + module;

// The Fast profile is deliberately not the Full coverage union. For a scoped
// change, add its reverse consumers; on conservative Full fallback, add known
// directly touched optional modules. The fixed native floor is never skipped.
export function additionalFastModules(plan) {
  const selected = plan.recommendation === "full" ? plan.changes.map(({ path: file }) => ownerOf(file)) : plan.affectedGoModules;
  return modules.filter((module) => selected.includes(module) && !covered.has(module));
}

// A successful go test with no matching tests, or a skipped real-engine test,
// is not evidence. Require actual PASS for each fixed package/test identity.
export function missingRequiredPasses(events, expected) {
  const passed = new Set(events.filter((event) => event.Action === "pass" && event.Test)
    .map((event) => event.Package + "/" + event.Test));
  return expected.filter(([pkg, name]) => !passed.has(prefix + pkg + "/" + name));
}

function go(args, { json = false } = {}) {
  const result = spawnSync("go", args, { cwd: root, encoding: "utf8", shell: false,
    stdio: json ? ["ignore", "pipe", "inherit"] : "inherit", maxBuffer: 64 * 1024 * 1024 });
  if (!json) {
    if (result.error || result.status !== 0) throw new Error(`go ${args[0]} failed: ${result.error?.message ?? result.status}`);
    return;
  }
  const events = (result.stdout ?? "").split("\n").filter(Boolean).map((line) => JSON.parse(line));
  for (const event of events) if (event.Output) process.stdout.write(event.Output);
  if (result.error || result.status !== 0) throw new Error(`go ${args[0]} failed: ${result.error?.message ?? result.status}`);
  return events;
}

function contracts(packages, pattern, expected, race = true) {
  const events = go(["test", ...(race ? ["-race"] : []), "-json", "-count=1", "-run", pattern, ...packages.map(goPath)], { json: true });
  const missing = missingRequiredPasses(events, expected);
  if (missing.length) throw new Error("required Fast tests did not pass: " + missing.map((pair) => pair.join("/")).join(", "));
}

export function runFast(plan, env = process.env) {
  if (!env.TEST_POSTGRES_DSN?.trim()) throw new Error("Fast CI requires actual TEST_POSTGRES_DSN");
  console.log(`Draft Fast CI: impact=${plan.recommendation}; Full merge gate remains separate.`);
  // Cover all Core packages except the long-running native Memory matrix.
  // Memory's original authority cases run with both actual database engines.
  const corePackages = execFileSync("go", ["list", goPath(core) + "/..."], { cwd: root, encoding: "utf8" })
    .trim().split("\n").filter((pkg) => pkg && pkg !== prefix + stores[0]);
  if (![core + "/kernel", core + "/agent", core + "/budget"].every((pkg) => corePackages.includes(prefix + pkg))) {
    throw new Error("incomplete Core package discovery");
  }
  go(["test", "-count=1", ...corePackages]);
  contracts([harness], "^Test(AgentSession|NamedAgent|GlobalOriginal|NativeAuthorityCutover|NativeTransitionCommit|OriginalDispatchReturn|FirstPartyLifecycle|CancellationProtocol|RootRetry|NoEntry|Managed|PR82)",
    [[harness, "TestAgentSessionToolApprovalAndOwnerBoundResolution"], [harness, "TestGlobalOriginalDispatchEvidenceFencesEveryMemoryAdmission"]], false);
  // Native Store/Host Store checks run with Race; the stable anchors work on
  // main and newer branches. New native authority suites are included as soon
  // as they exist. Full runs all original suites and all fault/expiry phases.
  contracts([...stores, ...hostStores],
    "^Test(EA1RootAgentAdmission(Memory|SQLite)|OwnedPhysicalMemory.*|OwnedPhysicalCallbackReturnMemory|PhysicalAdmissionCommitRollbackSQLite|RealPostgresPhysicalAdmissionCommitRollback|RealPostgresOwnedPhysicalBindingGuards|NoEntryCancellationSQLiteReopen|RealPostgresNoEntryCancellation|RealPostgresToolApprovalAndLedgerReopen|BudgetReplaysOriginalSettlementAfterReopen|ExecutionBudgetAtomic|RealPostgresExecutionBudgetAtomic|NativeAuthorityCutoverSQLite|RealPostgresNativeAuthorityCutover|OriginalDispatchReturnSQLiteReopen|RealPostgresOriginalDispatchReturn|NativeTransitionCommitSQLiteReopen|RealPostgresNativeTransitionCommit)$",
    [[stores[0], "TestEA1RootAgentAdmissionMemory"], [stores[1], "TestPhysicalAdmissionCommitRollbackSQLite"],
      [stores[2], "TestRealPostgresPhysicalAdmissionCommitRollback"], [hostStores[0], "TestNoEntryCancellationSQLiteReopen"],
      [hostStores[1], "TestRealPostgresNoEntryCancellation"]]);
  const retirement = ["TestOwnedRetirementMemoryStoreConformance", "TestSQLiteOwnedRetirementStore", "TestRealPostgresOwnedRetirementStore"];
  const managed = ["TestManagedMemoryStoreConformance", "TestSQLiteManagedRecoveryStore", "TestRealPostgresManagedRecoveryStore"];
  contracts(stores, `^(${[...retirement, ...managed].join("|")})$/(identity-retired-ID-reuse|identity-owned-borrowed-race|identity-rejected-admission-rolls-back|atomic-fresh-admission|callback-entry)$`,
    stores.flatMap((pkg, index) => [[pkg, retirement[index] + "/identity-retired-ID-reuse"], [pkg, managed[index] + "/callback-entry"]]));
  const additional = additionalFastModules(plan);
  if (additional.length) go(["test", "-count=1", ...additional.map((module) => goPath(module) + "/...")]);
  console.log("Draft Fast native and affected tests passed (not Full CI).");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runFast(collectImpact(impactContext(process.argv.slice(2)), { cwd: root }));
}
