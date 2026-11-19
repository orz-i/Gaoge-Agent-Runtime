# Check actual deliverables across three task families

```sh
go run ./go/agent-runtime-harness/examples/task-agent -mode=acceptance
```

This default is offline. It runs the **same Agent loop and Tools** used by the
opt-in Provider path, with a scripted action fixture. It emits a distinct
`task-outcome-acceptance-v1` report, never an `evidence-40-v1` quality score.

| Case | Actual work | Independent acceptance |
| --- | --- | --- |
| `code` | Read a buggy Go function/tests, execute tests, save a changed source file, test again | After the Agent finishes, introduce held-out tests and run them against the actual saved source |
| `documents` | Read a superseded draft and approved amendment; save an answer artifact | Check actual artifact facts and exact original paragraph reference; claiming completion is insufficient |
| `approval` | Request a native approval before creating a fixed scratch record; reconstruct Runner at the wait | Zero writes before decision; exactly one approved artifact; terminal Resume cannot write again |

The code task is deliberately narrow, **not a general code sandbox**. A model
may write only one bounded pure integer function: no imports, calls, loops,
globals, allocation or extra declarations. The AST is normalized without
comments/directives before execution. The command runs only the fixed Go test
invocation in its owned temporary module, disables network/module downloads and
CGO, and does not pass Provider credentials to Go. A trusted-source compilation
preflight happens before any model request. Public and held-out tests are
separate; a negative test proves that hardcoding public answers fails acceptance.

The approval case uses an explicit **fixture operator approval** in both modes.
It is not a human-interface rollout or authorization to perform real purchases,
network calls or business writes. All effects stay in the disposable workspace.

## Report interpretation

- `agentCompleted` and `outcomePassed` are intentionally different. A regression
  completes all Agent actions but saves draft facts; the outcome must fail.
- Actual artifact SHA-256 is recorded, not model self-reported correctness.
- `taskHash` covers task/Tool definitions, fixture and Oracle content and the
  execution ceilings. It is a declared identity, not a signature or source attestation.
- At most eight logical model steps and eight Tools per case, one attempt per
  call (no automatic retry), with a conservative model
  window assessment, a 64 KiB serialized-input ceiling and explicit output cap.
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

This delivery supplies one bounded task per family and executable Oracles. It
does **not** claim statistical significance, full production realism, controlled
A/B uplift or a broad coding/Agent benchmark. Fixture passes establish the
acceptance harness; real-model usefulness remains unmeasured until authorized
sampling. The old quality/profile/hash/report protocol is unchanged, and its
`review-quality` / `compare-treatment` commands must not accept this new report
as a historical 40-case score.
