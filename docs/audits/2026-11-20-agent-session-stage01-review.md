# Agent Session stages 0/1 review and remediation

Date: 2026-11-20. Source baseline: `19151c992084a003a236215b0a16066c4e73b370`.
Reviewed scope: the then-uncommitted `codex/agent-session-output-limits` worktree.
The review itself made no commit, merge, primary-checkout modification or later
Context-stage change. Subsequent submission is a separately authorized action.

## Findings and fixes

### Frozen Harness Config and actual native Run could disagree

A custom starter that drops or changes the newly supplied output ceiling could
return a valid native Run with ceiling 512 for a Turn whose sealed Config says
128. The former checks accepted that Start projection. A subsequently reopened
native Session could generate with 512 in prepared/pending and ordinary/managed
recovery paths, despite the original Config.

Harness now compares a positive original Config ceiling with the actual native
Root state at execution projection and continuation boundaries, including
managed recovery. Current handle settings do not supply this proof. Recovery
rejections occur without a generation or Run revision change. A rejected custom
Start may already have created its native Run; the mismatch is not treated as
proof that the custom callback had no effect. Legacy unspecified Config
zero remains compatible. Protective cancellation and terminal read projections
do not depend on this additional model policy check.

Regression: `root_output_limit_binding_test.go`; the mismatched Start and four
recovery cases reproduced before the fix. Positive-ceiling cancellation with
readable/unavailable Config and legacy-zero cases are also covered.

### Shared-budget recovery could remove or widen a per-call ceiling

Count-only reservations store zero requested output tokens. On a proven-unsent
retry, blindly restoring that zero erased the frozen positive ceiling; Agent
then correctly rejected the request as an invalid dispatch. Fresh token
reservations could similarly replace ceiling 64 with a Meter allowance of 512.

The reservation path now preserves the original positive ceiling when no token
allowance exists, and applies only tighter positive allowances. New token
reservations are clamped to the original ceiling. Existing reservation hashes,
physical-unsent proofs and charge/settlement authority are unchanged.

Regression: the existing proven-unsent claim fixture now also runs with ceiling
128. It failed before the fix with zero generations and
`agent.model_dispatch_invalid`. `budget_model_output_limit_test.go` covers
fresh/replayed token reservations with unspecified, smaller and larger ceilings.

### Pending eager Tool batches lost their Catalog binding

Consuming a ModelInvocation clears its Request and Catalog fingerprints. The
pending Tool calls formerly retained their IDs/keys/arguments but no original
definition proof. At `tool.batch_requested` commit-loss recovery, replacing the
same Key's Name, Terminal flag, schema or description executed the changed Tool.
All four mutations reproduced with one Tool effect before the fix.

Agent now copies call-ID-to-original-Catalog-hash evidence from the frozen
invocation into the same Tool-batch commit. Approval and execution validate the
registered definition against that evidence. The existing single Agent loop and
Store are retained; no parallel operation ledger is introduced.

Regression: `pending_tool_catalog_test.go` covers the four replacement cases,
unchanged definitions, Host schema projection, post-approval mutation and missing
proof. Old zero-ceiling batches without the new field retain the previous
recovery contract; bounded outstanding batches cannot silently downgrade to it.
No claim is made that a definition fingerprint authenticates executable Handler
code or repairs an unknown external outcome.

## Windows SQLite gate attribution

The original, unmodified standalone SQLite consumer was run against two isolated
local-cohort module graphs: an archive of baseline `19151c9` and the current
worktree. Both used `GOWORK=off`, `GOTOOLCHAIN=go1.27.4`, Windows/amd64, and the same
C: NTFS temporary-directory filesystem. Each ran `go mod tidy` and
`go test ./... -count=1 -v`.

| Evidence | Baseline | Worktree |
| --- | --- | --- |
| `go mod tidy` | Exit 0 | Exit 0 |
| Original consumer test | Exit 1 | Exit 1 |
| Failure | `runtime_test.go:37`, directory `Sync`: `Access is denied` | Same location and error |
| SQLite tracked source files | 82 files | All 82 byte-identical |
| Original consumer SHA-256 | `0a5ce32d058e76afbd5269b9942ce7117fbd9c36a185305ffa76ecb868069bb4` | Identical |

An independent standard-library probe successfully wrote and synced a regular
file and opened its containing directory, but directory `Sync()` returned
Windows errno 5 (`ERROR_ACCESS_DENIED`). The source path is
`sqlite.go` directory publication -> `os.File.Sync` -> Windows `FlushFileBuffers`.
This source inspection is not a captured native stack trace.

The failure predates this change in the tested environment. This finding does
not claim that every Windows host/toolchain behaves identically, and no SQLite
durability behavior is weakened or skipped to make the gate pass. The earlier
Linux standalone-consumer success is separate evidence, not a Windows pass.

Local raw evidence is retained under
`C:/Users/mouta/AppData/Local/Temp/gaoge-sqlite-windows-ab-vvYSGo/`:
`metadata.json`, `baseline-test.log`, `worktree-test.log`,
`directory-sync-probe.log`, `source-call-path.txt` and `summary.txt`.
The primary logs pin Go 1.27.4; separately retained initial automatic-toolchain
logs used Go 1.26.7 and are not substituted for the pinned comparison.

## Revalidation

- Core `go/agent-runtime` full module tests passed, including Memory conformance.
- Race tests passed for Agent, Handoff, Harness, HTTP and A2A.
- The advanced task-agent example's complete offline tests passed.
- Original PostgreSQL cross-process recovery passed with both explicit
  composition and `OpenAgentSession`, on pinned Go 1.27.4 with the same oracle.
- Original clean-consumer checks passed for Agent, Harness and HTTP in isolated
  `GOWORK=off` module graphs. No public wire shape changed during this review;
  the prior TypeScript 47-test and packed-consumer results remain applicable.
- Agent/Harness lint, batched repository Go formatting and diff checks passed.
- The Windows SQLite consumer comparison remains a documented failure on both
  sources, not a green full-Windows release/integration gate.

All test PostgreSQL containers used for this review are removed after validation.
At review completion, the primary `D:/Gaoge-Agent-Runtime` checkout was clean on
`main` at `19151c9`; the review had made no commit or merge.
