# Harness sealed Config and Invocation authority — I2/I3 convergence

## Immutable baseline and affected chain

- Independent audit baseline: `b47c7f60f552c67d22e5027240c40a9d84a9214d` (on `governance/harness-agent-minimal-hardcut-20261104`).
- Remediation commit: `1f29e72eef64c365ab81a4836ccc027c32f990a5`.
- This is a bounded **pre-stable hard cut** of existing private contracts, not a new Store, status, request router, migration or persisted authority.
- End-to-end scope: Host-sealed admission → Store CreateSession/PutConfigSnapshot/CreateTurn → verified Config read → CreateInvocation → canonical GetInvocation → Item/dispatch grant → Agent/Team/Workflow/Application/Handoff physical effects → context/budget/tool/request projection and recovery.

## Root cause and design decision

The previous repair checked `ConfigSnapshot.ID`, `TurnID` and a self-reported `ContentHash`, but did not **recompute** the hash from the actual immutable payload. Further, `CreateInvocation` returns were used for physical dispatch without comparing the complete original frozen execution identity. These were existing weaknesses, not introduced by the preceding top-level envelope refactor.

Keep the **Host's sealed request** as the single source for initial identity, and the original canonical frozen record as the recovery source. Centralize enforcement in `store_authority.go`:

1. `canonicalSealedConfig` re-runs the existing `SealConfigSnapshot` algorithm over a Store value, checking the actual derived **ContentHash and ID** against both stored claims. It returns the resulting normalized Config, rather than trusting uncanonical payload data. Legitimate Store JSON roundtrips can represent empty slices differently; there is no new payload schema or rewriting of historical state.
2. `loadSealedConfig` is the sole direct Harness `GetConfigSnapshot` call site. All 13 production read consumers use it, including Context, role and capability preparers, Budget/Approval, retry, Session/Turn views and admission validation. The initial `PutConfigSnapshot` returned receipt **and canonical Get** are checked before `CreateTurn`, budget Ensure, Invocation creation or physical effects.
3. `createVerifiedInvocation` is the sole direct Harness `CreateInvocation` call site. All four production call paths use it: top-level Agent, top-level composite, native child and Handoff child. It checks returned identity against the original candidate (`ID`, `TurnID`, parent Item, capability/version, execution class, `Input` bytes + hash, Run/ExecutionRefID, Attempt). A new record must remain `accepted`, Revision 1 with no forged terminal output. It then verifies the indexed `GetInvocation` record before granting Item/physical dispatch authority.
4. Existing Store CAS, grant/receipt, Kernel Run ownership checks, no-effect / unknown-effect fences, retry Attempt semantics and Provider request identities remain unchanged. The additional indexed Get is an admission integrity check, **not** a second execution lease or cross-transaction exactly-once guarantee.

## Independent RED → GREEN evidence

- Original isolated audit had RED cases for changed Model, ToolKeys, AdmissionHash with unchanged ContentHash, and a foreign persisted Invocation causing an actual physical Model invocation. Both occurred on pre-fix and preceding baseline, establishing them as inherited bugs.
- New permanent `TestHardcutConfigSnapshotRejectsUnsealedMutations`: 3 classes of tampered stored Config, with `ErrConflict` before any Invocation/Model call.
- New `TestHardcutCreateInvocationReceiptCannotSubstituteExecutionAuthority`: 13 classes of returned Invocation drift, including foreign persisted Invocation, stale InputHash, Attempt, Run ID, class/version, and forged fresh terminal status.
- New `TestHardcutForeignInvocationReceiptBlocksCompositeStart`: Team and Workflow root paths cannot use a foreign returned Invocation; post-review hardening records the actual Feature `StartRun` call counts and requires both to remain **zero**, not merely an eventual `ErrConflict`. The Agent/Team/Workflow identity-fence regressions passed five repeated Race runs.
- New read-side fault tests distinguish bad Put receipts, bad canonical Config reads, bad GetInvocation indexing, and corruption discovered **after an otherwise successful run**. Replays/Load fail closed without another Model call. A seeded 120-sample property-style mutation suite covers independent Model, Tool and AdmissionHash changes.
- Prior top-level Store receipt and cancellation/Retry negative regressions continue to pass. No raw `GetConfigSnapshot` or `CreateInvocation` calls remain outside the two centralized private validators and the Store implementations.

## Security invariants and limits

- **I2:** The meaning of the frozen Config is its canonical content hash, not the untrusted Store's string claim; model/tool authority cannot be expanded by altered payload fields that retain the original digest.
- **I3:** No Store return can substitute another Invocation's original physical identity before an Item, physical Grant, shared budget registration or Model/Tool call.
- The persisted Store **remains trusted** for atomic CAS and durable commits: a malicious adapter which lies consistently about both reads and physical effects cannot be made secure by these validations. A verified indexed read does not itself supply linearizability; Store conformance remains required.
- No new public API, migration, fallback bridge, schema version or generic status machine. A previously accepted self-contradictory Beta Config now fails closed until Host/operator repairs or quarantines it; no silent replay/migration.
- Real Provider/gateway behavior and unknown external effects remain separate acceptance gates. Physical exactly-once is not claimed.

## Test and release gate

- **PASS (code cohort):** Harness full suite, Harness `-race`, Harness PostgreSQL module, Go Vet/Lint (zero issues), and new focused negative/property tests. Complete `make check` subtargets were re-run as independent commands on commit `7926d1e`: metadata, baseline, formatting, tidy, vet, all Go modules' tests and Race runs, Go lint, TypeScript lint/typecheck/**46 tests**/build, evaluation/benchmark smoke, OTel/A2A and beta.24 clean-consumer release checks; all exited 0. A single uninterrupted `make check` exit code was **not** captured, so the individual subtargets are the actual local evidence.
- **PASS real-service integration:** dedicated PostgreSQL/Redis Docker project, `node scripts/run-integration.mjs --external-services` under `-race`, exit 0. Tested real Harness/Postgres stores, Redis queue delivery and continuation/Agent session recovery across process crashes. The `gaoge-agent-runtime-beta` PostgreSQL/Redis containers, network and associated test volumes were removed; unrelated Docker services remained running.
- **NOT YET RUN:** actual GitHub PR-head CI, live third-party Provider/gateway requests, production/mobile release acceptance. This branch has not been pushed and no PR has been created. The final test-only follow-up commit requires targeted verification before the next independent merge audit.
- **Residual trust boundary:** an arbitrary malicious Store that lies consistently about physical writes/reads remains outside these proof guarantees; integrity checks are not an adversarial database sandbox. Indexed validation does not replace Store CAS.
- Re-review the final diff for original Run, Tool, identity and cancellation invariants. If the same vulnerability class recurs, stop piecemeal patches and reassess Store trust/contracts before declaring merge admission.
