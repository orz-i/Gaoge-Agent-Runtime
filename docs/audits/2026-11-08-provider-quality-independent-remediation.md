# 2026-11-08 — Independent audit remediation: Provider quality evidence

## Scope and origin

- Review candidate: `governance/provider-quality-contract-20261108`, originally based on `main@f097ebd7919611b0a9cfb110c51d4661906adbf6`.
- Repair only the three independently reproduced findings in the first Provider capability / Agent-quality governance round. No new Provider HTTP, model calls, Kernel/Agent states, Tool execution, SQL migration or benchmarking profile changes.
- Existing original operator-owned ignored evidence reports remain immutable, including historical complete 37/40 (overall `failed`) and later clean 1/1 distractor-05 pilot (39 skipped). Different dataset hashes are never compared as an identical workload.
- Scope is the script-side reporting/verification trust boundary, not an independent re-audit of the completed remedy. The latter still requires a new review.

## Failure → repair → regression evidence

| Finding | Prior reproducible failure | Corrective contract | Independent negative/positive test |
| --- | --- | --- | --- |
| P2-01: hash-vs-parse TOCTOU | Hash verified bytes from one file read, then `baseline.read_report(path)` reopened a replaced file and admitted changed sample fields | `verify_evidence` reads one bounded byte snapshot, hashes **exactly those bytes**, then passes them into the existing strict protocol through `read_report_bytes`; no path reopen after hash | Deterministic file swap immediately after hashing: original source validation remains correct; new independent invocation rejects modified SHA |
| P2-02: impossible native Count duration | Three Count calls, total 100 ms, per-call max 1 ms incorrectly accepted | Under existing optional paired timing fields: `max <= total <= attempts * max`. All values are exact nonnegative integers within previous bounds; zero/zero remains valid; historical absent pair remains unknown | Exhaustive small oracle over 1–4 attempts, per-call max 0–8 ms and totals 0–16 ms; invalid tuples rejected, valid tuples retained |
| P3-03: unhandled report schema exception | Exact matching SHA for `{}`, invalid JSON or invalid UTF-8 produced unhandled `baseline.SafeFailure` | The verifier handles only the known strict-report protocol exception, maps it to fixed `invalid_evidence_report`, and returns CLI exit 1 without a local trace or untrusted content | Three malformed, hash-matching reports return controlled error, no traceback or file path |

The general `read_report(path)` entry now uses the same byte-snapshot validator and reads **at most `MAX_OUTPUT_BYTES + 1` bytes** from one opened file. This avoids an unsafe stat/read race and guarantees oversized inputs fail before parsing rather than allocating the entire file. The file-open error remains a fixed `invalid_report`, oversized input remains `report_too_large`; no permissive fallback or legacy report rewrite.

## Security boundaries unchanged

- Host-certified Provider media grants, model-specific frozen binding fingerprints, Agent ModelInvocation and Kernel CAS/lease continue to govern *physical* effects independently of these report diagnostics.
- Token Count timing is observational local wall time, not proof of physical HTTP, model-call time, billable usage, Context budget or success rate. Invalid observations cannot enter published new reports.
- The strict report loader still rejects duplicate JSON keys, malformed/case-inconsistent evidence, foreign schemas and incorrect sample coverage. The verifier never reads the raw file a second time after the manifest's recorded content SHA-256 is checked.
- Existing private report bytes/credential material are neither modified nor committed, printed nor embedded in the manifest.

## Validation and outstanding gates

- RED verification before the fix: swap-after-hash test rejected an otherwise valid original snapshot; malformed hash-matching reports raised unhandled errors; the duration property oracle admitted impossible sums.
- GREEN verification after fix: **131** Python Agent test cases passed; the two original live report files were validated against their recorded SHA-256 and public facts; static evidence manifest validation passed; `git diff --check` passed.
- Post-remediation complete local `make check`: **PASS, exit 0**, including Go workspace unit/Race/Vet/Lint, TypeScript lint/typecheck/46 tests/build, evaluation/benchmark/OTel smoke, A2A product and beta.24 clean-consumer release check. The ignored local log is `coverage/provider-quality-round1/remediation-check.log`, with its explicit exit receipt in `remediation-check.exit`. This check applies to the uncommitted governance worktree; it is not GitHub PR-Head CI.
- Separate real PostgreSQL/Redis integration, signed external Provider tests, paid live Agent sampling, final-commit GitHub CI and an independent **post-remediation** code review have **not** been performed here. The original 40-case quality dataset hash and historical task outcomes are unchanged by this repair.
