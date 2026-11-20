# Check actual deliverables across three task families

```sh
go run ./go/agent-runtime-harness/examples/task-agent -mode=acceptance
```

This default is offline. It runs the **same Agent loop and Tools** used by the
opt-in Provider path, with a scripted action fixture. It emits a distinct
`task-outcome-acceptance-v2` report (seven ordered variants in three task
families), never an `evidence-40-v1` quality score. The actions are scripted;
this measures the Executor/Context/receipt/approval/oracle wiring, not model
tool selection or reasoning capability.

| Case | Actual work | Independent acceptance |
| --- | --- | --- |
| `code`, `code-negative`, `code-degenerate` | Repair Clamp under separate positive, negative-only and zero-width/boundary public cases | Each variant has separate held-out tests against the final saved source; re-editing after passing public tests invalidates that evidence |
| `documents`, `documents-distractors` | Read the superseded draft and approved amendment; the second variant also has a conflicting unsigned vendor note and an irrelevant access note | Check actual artifact facts and original paragraph reference; distractor case inspects both decoys; a plausible wrong answer fails |
| `approval`, `approval-denied` | Reopen a waiting Run in a new Runner; one variant approves and the other rejects | Zero writes before decision; approval writes once; rejection creates no artifact, preserves the rejected-call receipt and reports denial |

The code task is deliberately narrow, **not a general code sandbox**. A model
may write only one bounded pure integer function: no imports, calls, loops,
globals, allocation or extra declarations. The AST is normalized without
comments/directives before execution. The command runs only the fixed Go test
invocation in its owned temporary module, disables network/module downloads and
CGO, writes Go build/module caches only under that owned temporary workspace,
and does not pass Provider credentials to Go. A trusted-source compilation
preflight happens before any model request. Public and held-out tests are
separate; a negative test proves that hardcoding public answers fails acceptance.

The approval cases use an explicit **simulated operator decision** in both
modes; the suite does not assess a human approval UI. The approval variant
approves; the denial variant checks the original rejected Tool receipt, truthful
denial answer and absence of a file. This is not authorization for real
purchases, network calls or business writes. All effects stay in the disposable
workspace.

## Report interpretation

- `agentCompleted` and `outcomePassed` are intentionally different. A regression
  completes all Agent actions but saves draft facts; the outcome must fail.
- Actual artifact SHA-256 is recorded, not model self-reported correctness.
- `taskHash` covers the exact variant documents/source, Tool definitions,
  Oracle/held-out tests, selected model/profile/endpoint identity, Go version,
  input/output/model/tool/retry limits and approval-policy version. `sourceRevision`
  and `sourceDirty` are separate report metadata. Hashes are declared identities,
  not signatures, source attestations or outcome receipts.
- The report names ordered planned case IDs and keeps planned family counts
  separate from processed counts. On failure, it contains only the attempted
  prefix; later cases are not counted as attempts.
- At most eight logical model steps and eight Tools per case, one attempt per
  call (no automatic retry), with a conservative model window assessment, a
  64 KiB serialized-input ceiling and explicit output cap.
- On failure, later cases are not started. Requested, processed (including setup
  failures), and actually started Agent-case counts stay separate. A missing Go
  toolchain is not reported as a model's failed code task.
- Reports omit raw model answers, source bodies, endpoints, credentials, Run IDs
  and error bodies. Known consumed model steps are **not a bill or a count of
  every possible failed remote request**.
- `durationMs` includes setup and the independent Oracle; it is not pure
  inference latency. `completeGenerationTokens` is null unless every invocation
  has consumed known Usage. Missing Usage is never converted into zero or price.

## Optional actual-model pilot — may incur charges

No live sampling is run by tests or default commands. After separate operator
authorization, supply the existing Host-certified model profile catalog and
secret environment variable, then choose one case:

```text
go run ./go/agent-runtime-harness/examples/task-agent -mode=acceptance \
  -acceptance-case=documents -provider=openai -model=YOUR_EXACT_MODEL_ID \
  -context-tokens=YOUR_DOCUMENTED_CAPACITY -max-output-tokens=1024 \
  -allow-live-calls
```

`-allow-live-calls` is mandatory. Without it, validation returns before model
construction or transport; the negative test installs a trapping transport.
`OPENAI_API_KEY` / `ANTHROPIC_API_KEY` and `GAOGE_MODEL_PROFILES_FILE` retain the
existing example's explicit Host configuration contract. Do not put keys in
argv or the report. Pin and record the source commit, model/route, profile,
prompt/treatment and repeated/held-out workload when collecting real evidence.

This delivery supplies seven fixed variants across three bounded task families
and executable Oracles. It
does **not** claim statistical significance, full production realism, controlled
A/B uplift or a broad coding/Agent benchmark. Fixture passes establish the
acceptance harness; real-model usefulness remains unmeasured until authorized
sampling. The old quality/profile/hash/report protocol is unchanged, and its
`review-quality` / `compare-treatment` commands must not accept this new report
as a historical 40-case score.
