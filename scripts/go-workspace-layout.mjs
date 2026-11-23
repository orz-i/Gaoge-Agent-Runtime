export const modules = [
  "go/agent-runtime",
  "go/agent-runtime-provider",
  "go/agent-runtime-harness",
  "go/agent-runtime-harness/examples/task-agent",
  "go/agent-runtime-harness-postgres",
  "go/agent-runtime-harness-sqlite",
  "go/agent-runtime-mcp",
  "go/agent-runtime-otel",
  "go/agent-runtime-a2a",
  "go/agent-runtime-acp",
  "go/agent-runtime-postgres",
  "go/agent-runtime-sqlite",
  "go/agent-runtime-redis",
  "go/agent-runtime-http",
  // Independent module: validated, but not part of the Runtime tag cohort.
  "go/provider-transport",
];

// P0 run 44533727890: split the 938-second race suite into approximately
// 280/330/328-second groups. These are scheduling hints, never test filters.
export const shards = {
  core: ["go/agent-runtime", "go/agent-runtime-mcp", "go/agent-runtime-otel",
    "go/agent-runtime-a2a", "go/agent-runtime-acp", "go/agent-runtime-postgres", "go/agent-runtime-redis"],
  sqlite: ["go/agent-runtime-harness-sqlite", "go/agent-runtime-sqlite"],
  adapters: ["go/agent-runtime-provider", "go/agent-runtime-harness",
    "go/agent-runtime-harness/examples/task-agent", "go/agent-runtime-harness-postgres",
    "go/agent-runtime-http", "go/provider-transport"],
};

export function validateLayout(groups = shards) {
  const selected = Object.values(groups).flat();
  if (new Set(selected).size !== modules.length || selected.length !== modules.length ||
      modules.some((module) => !selected.includes(module)) ||
      Object.values(groups).some((group) => !Array.isArray(group) || !group.length)) {
    throw new Error("Go shards must partition every workspace module exactly once");
  }
}

export function selectModules(mode, args) {
  validateLayout();
  if (!args.length) return { selected: modules, shard: null };
  const match = args.length === 1 && /^--shard=([a-z]+)$/u.exec(args[0]);
  const shard = match?.[1];
  if (!["test", "race", "coverage"].includes(mode) || !Object.hasOwn(shards, shard ?? "")) {
    throw new Error("only test/race/coverage accept one known --shard=core|sqlite|adapters");
  }
  return { selected: modules.filter((module) => shards[shard].includes(module)), shard };
}

export const coverageProfileName = (module) => `${module.split("/").at(-1)}.out`;
