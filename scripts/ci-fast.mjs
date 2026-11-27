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

// The native transition matrix is introduced as a shared family on PR157.
// Only one commit/readback cut is part of Fast. A missing whole family on
// the older baseline is explicit; a partial family or missing selected cut
// on a branch that has the suite must not pass.
function transitionSmoke(packages, names, race) {
  const events = go(["test", ...(race ? ["-race"] : []), "-json", "-count=1", "-run",
    `^(${names.join("|")})$/^continue$/^after$`, ...packages.map(goPath)], { json: true });
  const expected = packages.map((pkg, index) => [pkg, names[index]]);
  const present = expected.filter(([pkg, name]) => events.some((e) => e.Action === "run" && e.Package === prefix + pkg && e.Test === name));
  if (!present.length) {
    console.log("NativeTransitionCommit family not present on this baseline; other required native anchors ran.");
    return;
  }
  if (present.length !== expected.length || missingRequiredPasses(events, expected.map(([pkg, name]) => [pkg, name + "/continue/after"])).length) {
    throw new Error("native transition family or selected commit/readback cut incomplete");
  }
}

export function runFast(plan, profile = "all") {
  if (!["all", "core", "native"].includes(profile)) throw new Error("unknown Fast profile");
  if (profile !== "core" && !process.env.TEST_POSTGRES_DSN?.trim()) throw new Error("Fast native requires actual TEST_POSTGRES_DSN");
  console.log(`Draft Fast CI: profile=${profile}; impact=${plan.recommendation}; Full merge gate remains separate.`);
  if (profile !== "native") {
    // Cover all Core packages except the long-running native Memory matrix.
    // Memory's original authority cases run with both actual database engines.
    const corePackages = execFileSync("go", ["list", goPath(core) + "/..."], { cwd: root, encoding: "utf8" })
      .trim().split("\n").filter((pkg) => pkg && pkg !== prefix + stores[0]);
    if (![core + "/kernel", core + "/agent", core + "/budget"].every((pkg) => corePackages.includes(prefix + pkg))) {
      throw new Error("incomplete Core package discovery");
    }
    go(["test", "-count=1", ...corePackages]);
    contracts([harness], "^Test(AgentSession|NamedAgent|GlobalOriginal|NativeAuthorityCutover|OriginalDispatchReturn|FirstPartyLifecycle|CancellationProtocol|RootRetry|NoEntry|Managed|PR82)",
      [[harness, "TestAgentSessionToolApprovalAndOwnerBoundResolution"], [harness, "TestGlobalOriginalDispatchEvidenceFencesEveryMemoryAdmission"]], false);
    transitionSmoke([harness], ["TestNativeTransitionCommitMemory"], false);
    const additional = additionalFastModules(plan);
    if (additional.length) go(["test", "-count=1", ...additional.map((module) => goPath(module) + "/...")]);
  }
  if (profile !== "core") {
    // Native Store/Host Store checks run with Race; the stable anchors work on
    // main and newer branches. New native authority suites are included as soon
    // as they exist. Full runs all original suites and all fault/expiry phases.
    contracts([...stores, ...hostStores],
    "^Test(EA1RootAgentAdmission(Memory|SQLite)|OwnedPhysicalMemory.*|OwnedPhysicalCallbackReturnMemory|PhysicalAdmissionCommitRollbackSQLite|RealPostgresPhysicalAdmissionCommitRollback|RealPostgresOwnedPhysicalBindingGuards|NoEntryCancellationSQLiteReopen|RealPostgresNoEntryCancellation|RealPostgresToolApprovalAndLedgerReopen|BudgetReplaysOriginalSettlementAfterReopen|ExecutionBudgetAtomic|RealPostgresExecutionBudgetAtomic|NativeAuthorityCutoverSQLite|RealPostgresNativeAuthorityCutover|OriginalDispatchReturnSQLiteReopen|RealPostgresOriginalDispatchReturn)$",
    [[stores[0], "TestEA1RootAgentAdmissionMemory"], [stores[1], "TestPhysicalAdmissionCommitRollbackSQLite"],
      [stores[2], "TestRealPostgresPhysicalAdmissionCommitRollback"], [hostStores[0], "TestNoEntryCancellationSQLiteReopen"],
      [hostStores[1], "TestRealPostgresNoEntryCancellation"]]);
    const retirement = ["TestOwnedRetirementMemoryStoreConformance", "TestSQLiteOwnedRetirementStore", "TestRealPostgresOwnedRetirementStore"];
    const managed = ["TestManagedMemoryStoreConformance", "TestSQLiteManagedRecoveryStore", "TestRealPostgresManagedRecoveryStore"];
    contracts(stores, `^(${[...retirement, ...managed].join("|")})$/(identity-retired-ID-reuse|identity-owned-borrowed-race|identity-rejected-admission-rolls-back|atomic-fresh-admission|callback-entry)$`,
    stores.flatMap((pkg, index) => [[pkg, retirement[index] + "/identity-retired-ID-reuse"], [pkg, managed[index] + "/callback-entry"]]));
    transitionSmoke(hostStores, ["TestNativeTransitionCommitSQLiteReopen", "TestRealPostgresNativeTransitionCommit"], true);
  }
  console.log(`Draft Fast ${profile} contracts passed (not Full CI).`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const profile = args[0]?.startsWith("--profile=") ? args.shift().slice("--profile=".length) : "all";
  runFast(collectImpact(impactContext(args), { cwd: root }), profile);
}
