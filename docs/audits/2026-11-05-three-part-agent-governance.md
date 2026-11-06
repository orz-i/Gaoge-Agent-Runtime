# 2026-11-05 — Three-part Agent/Provider/quality governance acceptance

## Scope and authority

- Base: `main@f2999a784ab272728ed5a6ee1eee680de28ee855` (merged PR #89).
- Development branch: `governance/agent-provider-quality-20261105`.
- Changes are **pre-stable, backward-compatible where fields are omitted**.
  No new Run/Turn/Tool persistent status, Kernel state machine, Feature
  router, model/Tool loop, SQL DDL, schema increment or compatibility bridge.
- Existing physical-effect invariants: Host chooses model/feature, Agent
  seals a single ModelInvocation including original Provider binding and
  `ModelOptions`; unknown physical Tool or Model effects are not retried
  without their original receipt/no-effect proof. Kernel CAS/Epoch/lease and
  Harness Turn/Invocation identity gates remain mandatory.
- Original untracked user-owned OpenAI Responses probe scripts were not
  edited or committed.

## Slice 1 — capability-checked reasoning effort

- Optional `ModelOptions.reasoning.effort` is mapped into OpenAI Responses
  `reasoning.effort` or Anthropic Messages `output_config.effort`.
- Exact model capability and Host-certified private-model overrides must
  permit the level *before* HTTP; unknown values, duplicate/folded JSON
  keys, null/unknown options and attempted wire injection are rejected.
- Strict JSON Schema `output_config.format`, original frozen model request,
  Tool declaration and encrypted/signed ProviderReplay rules are unchanged.
- The normalized private model certification is copied and bound to the
  existing Provider binding HMAC fingerprint; changing that certification
  cannot silently resume a pending invocation.
- OpenAI GPT-6/5.6 exact support and Anthropic per-model effort differences
  were cross-checked against their published API references. Claude Sonnet
  4.6 supports `max` but not `xhigh`; this is now tested.
- This slice does **not** add native Provider-hosted Tools, native Provider
  server-side Context compression or third-party proxy compatibility claims.

## Slice 2 — minimal authority convergence

- Shared `SessionClient.authorizedTurnID` reuses exactly the previous
  immutable Host Turn lookup and Session Actor proof for `Load`,
  `LoadState`, `Timeline`, `Refresh`, and `Resume`.
  `Prompt` retains its special before-transcript identity check.
- Cross-Actor denial is tested for all read/resume paths, including zero
  additional physical Model effects after a foreign Resume.
- Explicitly **rejected** a proposed `Agent.repeatedUnchangedToolKeys`
  signature-index optimization. Its O(n) theoretical behavior improved
  long unique-result histories but regressed repeated-result histories
  due to map materialization/allocations. Production algorithm was
  reverted rather than adding a cache, secondary policy state or
  complexity with only partial performance benefit.

## Slice 3 — reproducible effort/quality/performance comparison

- The real-model Agent baseline optionally accepts and reports
  `reasoningEffort`, preserving the omission semantics and historical
  report schema. It is bound into the existing frozen Session Config and
  the schema/tool-replay preflight requests, not appended in mutable
  dispatch middleware.
- Unsupported effort and unknown-model cases are rejected *before HTTP*;
  two script-defined task cases, original recovery oracle, Tool count,
  immutable identities and strict replay are retained.
- Comparison prints the effort setting. Different effort alone may be
  compared numerically when task corpus/profile/output tokens/repetitions
  match; missing effort means **provider default**, not a guessed level.
  Incomplete/failed samples and unknown Token Usage remain visible.
- New independent 160-seed randomized policy oracle plus competing
  repeated/unique Tool-result microbenchmarks; `make
  benchmark-agent-policy` is **report-only** on local hardware and is not
  an automatic global performance claim.

## Local verification on this branch

- Provider module: `go test -race ./... -count=1` — **PASS**.
- Harness module: `go test -race ./... -count=1` — **PASS**.
- Task-agent: `go test -race ./examples/task-agent -count=1` — **PASS**.
- Python baseline: `make agent-baseline-test` — **PASS**, 62 tests.
- Agent policy: `make benchmark-agent-policy` — randomized oracle and
  two workload families **PASS**, measurements report-only.
- Full-workspace `make check` began and passed metadata,
  format/tidy/vet, all module unit tests, and Core/Provider/Harness
  Race, but its Mac Pi transport was disconnected at the task-agent
  Race step. **It did not return a full successful exit.**
- Remaining Go modules' Race suites were re-run manually and **PASS**;
  `go-lint` 0 issues for each module, TypeScript lint/typecheck/46 tests
  and build, evaluation/A2A/MCP corpus, portable Benchmark smoke,
  OTel smoke, A2A product gate and clean-consumer release gate all
  passed as individual original Make targets.
- Real PostgreSQL Kernel/Harness, Redis and PostgreSQL/Redis
  Continuation E2E `-race` tests **PASS** against temporary local
  Docker Compose services; all temporary test containers/volumes/network
  were removed afterward. They did not affect other Docker projects.
- No paid live OpenAI/Anthropic API call, third-party gateway test,
  output-quality statistical comparison, production traffic,
  actual crash/power-loss or GitHub branch CI was run; such claims
  require separately verified external credentials and environments.

## Exit decision

These three bounded governance slices are locally ready for a separate
code review and PR/CI admission. This record is a local engineering
acceptance and is **not** an independent third-party audit, real-model
quality ranking, remote CI verdict, release, merge or production approval.
