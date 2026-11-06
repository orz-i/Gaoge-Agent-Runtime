# 2026-11-06 — Independent Session owner follow-up remediation

## Review scope

- Target reviewed SHA: `131c9f2f94bdc0815604de906eb6e4529442b904` on branch `governance/agent-provider-quality-20261105`.
- Independent read-only red reproductions: Mac Pi directory `/private/tmp/gaoge-final-review-131c9f2.yEHQPP/`; `session-active.log`, `session-persistent-owner.log` and `anthropic-public-effort.log`.
- This correction addresses **one conditional P1 authorization defect** in the Session convenience facade and **one P3 exact public-model capability omission**. Neither introduces new Kernel state, storage schema, a compatibility bridge nor a second Agent continuation protocol.

## Invariants and boundaries

1. A SessionClient's Host-selected `Actor`, tenant-scoped `HostThread`, Kernel `Thread`, Turn identity and *original immutable* root Agent Invocation must agree before any private read or physical continuation. A single syntactically valid `GetSession` response does not independently prove ownership.
2. Root Invocation `InputHash` and the immutable Actor/Thread in `Input` are checked at the entrance to every SessionClient read/recovery path. The actual returned complete snapshot is checked a second time against its caller; a paged Timeline re-verifies its frozen owner after reading Items, since Items carry TurnID rather than a standalone Actor proof.
3. `SessionClient.Resume` passes the caller Actor/Thread through the existing `Runner.ResumeTurn` implementation and compares the actual reloaded root, Session and independently persisted Kernel Run with that caller **before** `Agent.Resume`, Tool or Provider effect. The direct Host-authenticated `Runner.ResumeTurn` is unchanged.
4. Accepted Turns without a verifiable original root Invocation do not gain SessionClient read authorization from a mutable Session row; return `ErrConflict`. An original interrupted `Prompt` may be retried with the same admission identity through its normal path.
5. `SessionClient.LoadState` remains a bounded, read-only Harness projection and does not call Kernel. Host transcript lookup remains the Host's own authenticated boundary. Custom Stores must preserve immutable Session and root Invocation identities as specified by the Store contract. This is not a claim that a fully compromised Host Store and Kernel can be trusted.
6. Anthropic's documented `claude-mythos-preview` effort capabilities are `low`, `medium`, `high`, `max` (no `xhigh`); an invalid effort/sampling combination fails *before* both Unary and SSE physical HTTP dispatch.

## P1 correction

`go/agent-runtime-harness/session_client.go`:

- `authorizedTurnID` still checks the stable Host Session before anything private, but now also requires the original root Agent Invocation's Actor **and Kernel Thread** to match the caller, using the established `frozenTopLevelInvocationIdentity`/hash validator and exact Turn/Session binding. The root ID is derived from the previously frozen Session Turn/Request ID and loaded by indexed `Store.GetInvocation` rather than an unbounded `ListInvocations` scan.
- `Load`, `Refresh` and `Resume` validate the actually returned snapshot against the caller's HostThread, Actor, Turn, frozen Config and root Invocation, even when a failed continuation returns a partial snapshot alongside an error. Session-scoped `Refresh` also requires the actual current root owner **before** calling any Subtask/Item repair code.
- `LoadState` validates the actual returned bounded owner, and `Timeline` rechecks the frozen owner after the page read. No extra persistent identity source was invented.
- An accepted Turn without an original root Invocation is intentionally inaccessible via this facade; direct authorized Runner reconciliation remains possible.

`go/agent-runtime-harness/resume_turn.go`:

- Private, scoped `resumeTurnForSession` calls the existing `resumeTurn` implementation with the expected Host Actor/Thread. The public `ResumeTurn` still calls it without a caller argument, preserving explicit Host authentication.
- Before terminal projection, pending interaction handling or any new physical execution, the actual root Invocation identity must match the scoped caller. A subsequent Session read and independently loaded Kernel Run must **also** match immediately before executing `resumeInvocationExecution`. This closes the first-read-valid, later-read-foreign ModelInvocation path and does not bypass the Agent's existing RetryAfter, approval, usage or unknown-effect fences.

**Permanent regressions:**

- `session_actor_binding_negative_test.go`: foreign Actor with one-shot fake Session Actor and persistent fake Session Actor cannot obtain `Load`, bounded state, Timeline, Refresh or Resume; the originally undispatched ModelInvocation is untouched and its actual owner can resume it exactly once.
- `session_actor_frozen_root_swap_test.go`: even simultaneous valid-looking first GetSession and first *indexed* GetInvocation Input+Hash substitution cannot authorize a later genuine foreign root; guards return `ErrConflict` for all five paths, no Provider requests, no cross-actor Timeline reads/repair writes during `Refresh`, legitimate owner subsequently resumes.
- `session_frozen_owner_edge_test.go`: same Actor but wrong Kernel Thread is rejected; accepted startup gaps without root Invocation fail closed instead of inferring an owner from a Session response.

## P3 correction

The exact public model ID `claude-mythos-preview` is now included alongside Sonnet/Opus 4.6 in the Anthropic `low/medium/high/max` branch. The existing `knownNoSampling` classification remains consistent. `TestMythosPreviewEffortMaxWithoutXhighBeforeUnaryAndSSE` verifies model capability, exact outbound `output_config.effort` on both wire types, and pre-HTTP rejection of `xhigh` and unsupported sampling.

Authority: [Anthropic effort documentation](https://platform.claude.com/docs/en/build-with-claude/effort) — Mythos Preview listed with `max` but not `xhigh`.

## Verification and exit

The original independent red tests were rerun using Go `-overlay` before staging and turned green; expanded permanent matrix tests cover indexed root reads, frozen Thread drift, missing roots, forged Session plus independently forged root read receipts, zero physical model dispatch, pre-repair no-Item-access, and subsequent legitimate resumption.

The acceptance tests in the correction working tree passed:

- Full Harness and Provider unit suites; Core Agent/Kernel/JSON tests.
- Full `go test -race ./... -count=1` across **all 12 Go workspace modules**: Core, Provider, Harness, task-agent, harness-postgres, PostgreSQL, Redis, SQLite, MCP, A2A, OTel and HTTP — PASS. The direct Session facade adversarial subset was rerun under Race after final assertions.
- `make metadata fmt-check tidy-check go-vet go-lint`: PASS, Go lint **0 issues** throughout the 12-workspace-module cohort.
- `make ts-lint ts-typecheck ts-test ts-build`: PASS, **46 TypeScript tests**.
- `make eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check`: PASS; beta.24 clean-consumer check passed.
- Python `scripts/test_agent_baseline.py`: **62 tests** PASS.
- `make integration`: PASS, exit code **0** against ephemeral Docker PostgreSQL/Redis, including real PostgreSQL cross-connection/cross-process Session and Continuation recovery; dedicated project services, volumes and network removed.

The individual targets of the repository's `make check` were verified separately; no uninterrupted aggregate `make check` exit code is asserted. The final committed SHA still requires a metadata/consumer and clean-diff verification, then a fresh independent final audit and GitHub PR-Head CI before merge. There is no current PR Head GitHub CI, paid live Provider call, real power-loss test or external auditor signoff. A fully malicious Store that can permanently forge all immutable Session/root receipts remains outside the Store interface integrity contract; independent Kernel Run validation protects physical Agent continuation when the Run exists.

This is a **local repair candidate**, not an independent final-code sign-off. For merge admission: run a fresh independent review on the immutable last SHA, convert any new material findings to regressions, and require the actual PR Head's GitHub CI; do not interpret a local test green as proof of external Provider exactly-once billing or of a fully malicious data-store compromise.
