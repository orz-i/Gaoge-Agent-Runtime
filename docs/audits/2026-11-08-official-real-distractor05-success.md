# 2026-11-08 — Official real-model Provider/Context + complex evidence pilot acceptance

## Verdict and immutable source

**Single-case targeted acceptance PASSED**, not a full 40-case benchmark.

The original operator-run official OpenAI Responses `gpt-5.6-terra`
report `artifacts/agent-baselines/20261108T073605Z-97c7b1ea.json` has
SHA-256 `228785b209571def2cf67e50d541eadbb9b36edc3fe1070714c19f7399047f03`,
and is **not committed, rewritten or reclassified**. The report was
validated by the strict Python report loader, rather than taking its
terminal summary on faith. Its source was **clean** Head
`b219b837d6cda10fea691e4829b72dd9da7c627b`, its task version was
`release-evidence-quality-v1` and its frozen quality dataset hash was
`sha256:9e6a18425ab3ce620e9b65d864626855f60dcd860533741154fc29c364f9f114`.
It used the certified official `openai` `gpt-5.6-terra` endpoint,
reasoning effort `high`, context capacity 1,050,000 and output cap
128,000 (a **maximum**, not consumed output). The Host's Context input
limit remained 32,768.

The preflight passed strict JSON Schema over HTTP 200. The two-call
synthetic Tool continuation passed, and **both reasoning and encrypted
replay were observed** in this report (unlike the previous failed
live run, whose replay probe had observed neither).
Preflight usage is separate from task usage.

### Original task facts

| Fact | Observed |
| --- | --- |
| Fixed pilot identity | `pilotOnly=true`, `pilotCaseID=distractor-05`, task `distractor-05-01` |
| Sample coverage | requested 40, attempted **1**, passed **1**, skipped **39**, `complete=false` |
| Result and Oracle | `pilot_sample_passed`, sample `passed`, `stage=completed`, `oraclePassed=true` |
| Tool receipts | **1** `lookup_release`, **1** exact query-backed `read_context_artifact`, **1** `save_draft` |
| Retrieval modes | queried=1; offset-only=0; candidate/normalized/offset-hint pages=0 |
| Actual tool use | 3 of the frozen hard limit **10**, seven calls spare |
| Model calls | 4, all 4 with accepted known Usage |
| Native count calls | 5 attempts, **5 accepted**, **0 unavailable**, 6-way failure bins all zero |
| Same-input count vs Usage | **4 compared, 4 equal, 0 below, 0 above, maximum observed undercount 0** |
| Task usage | input 3,672 Tokens, output 256 Tokens, reasoning 36 Tokens; cache-read 0 |
| Recovery | deliberate interruption observed, lease fenced, same Run and Turn, committed Tools not replayed, no extra Tools after resume and terminal resume no-op — all true |

The unmatched fifth counting attempt is **not** misreported as a
physical generation, cost or validated comparison. The four
successful, identity-matched model-call comparisons give positive
evidence for official Provider native input-count accuracy on this
single workload and model/endpoint; they do not establish universal
accuracy for other models, tasks, gateways, or an external billing
invoice. Neither `maxOutputTokens=128000` nor the input reservation
is a statement of actual usage.

All three successful Tools are identified by their **recorded
Handler execution receipts**, not merely by a model's function-call
text. The genuine committed `save_draft` and all seven recovery
checks prove the expected Agent interruption/resume workflow and
Oracle; this is stronger than a synthetically plausible final answer.

## Comparison and non-claims

The previous genuine single-case pilot on the prior quality hash
`sha256:5d8827e9cb5f4394f35fc0a675f1376e6e2a354d535536008add0012f244338c`
was correctly reported as **FAILED** with trusted
`agent.tool_limit`: one lookup plus nine Artifact reads, zero drafts.
Its immutable report
`20261108T065436Z-131437a3.json` SHA-256 is
`a581b069d9659547716ebe9bfdbfc639b71ffad4b74de2e4a9fa90a73352f49e`.
The updated quality Host now instructs the model to prioritize the
full distinctive identifier supplied by the user, verify original
source and reserve one of the existing 10 Tools for a draft.
The improved **3-Tool** observed path and passing live Oracle establish
that this particular revised pilot succeeded. Because the
instructions deliberately changed and the dataset hash rotated,
**do not interpret one old failure and one new success as a
statistically meaningful same-profile pass-rate improvement**.

The initial official complete 40-case benchmark (37/40 on its
earlier contract) and previous failed single-case runs are all
preserved as original historical observations; the present run
attempted **one** case, not 40, and its apparent sample pass rate
cannot be presented as the 40-case suite success rate.
The scripted 40/40 confirms Tool/Context/Oracle mechanics only.

## Final independent boundary check

Security and identity invariants reviewed on the source committed
before the live call:

1. Frozen Host-certified Provider/model identity and byte-exact signed
   Tool/reasoning replay remain enforced by native counting. Count
   requests are read-only, not generation or a permission grant.
2. Native Count errors retain content-free classes and conservative
   hard-byte fallback. Positive count receipts can match **at most
   one** completed same-input model call and cannot be reused on
   failure/missing Usage. The input correspondence digest never
   enters reports or durable Kernel state.
3. Source evidence comes from an authenticated Context Artifact,
   constrained by Run/Turn/Session identity and content hash. Candidate
   previews/normalized hints are not accepted source facts.
4. The Agent's Tool limit, draft effect receipt, interruption, lease
   fence and same Run/Turn recovery are not bypassed by the Host. The
   original hard limit of 10 includes the committed draft.
5. Report protocol enforces one fixed pilot case, immutable source
   and dataset identity, correct summary coverage, consistent Tool
   executions, Usage/counter partitions, trusted failure enums and
   private redaction. A previous failed report is never converted
   into a success.

**Independent authenticated human/model code review is not implied
by this boundary review.** GitHub CodeQL, dependency, mandatory CI and
coverage checks are separate automated acceptance evidence; skipped
OpenTelemetry Collector E2E must remain disclosed.

## Launcher text correction after the successful run

The successful run revealed a purely user-facing discrepancy:
the launcher printed `本次仅试运行固定案例 code-01` even when the
command line selected `pilot-distractor`, but the child mode, frozen
`pilotCaseID`, actual sample and report correctly selected
`distractor-05`. The launcher now forwards the single trusted
selected pilot ID to both the interactive and historical-report
configuration banner and the execution invocation. Any other pilot
case is rejected; no model prompt, Tool schema, sample selection,
oracle, measurement policy or dataset hash was changed.
Python tests cover both CLI modes, historical/interactive
configuration, correct banner and disallowed case IDs without
sending a paid request.

## Acceptance boundary

The **named** Provider/Context native metering and complex evidence
`distractor-05` single-case pilot are now supported by an actual
successful official live report. Further independent reproduction
on other models/tasks and a complete **40-case current-profile** run
remain **unmeasured**, not implicit passes. The new banner-only
patch must pass its **exact PR Head** mandatory GitHub CI/CodeQL,
and the repository's required review policy must be respected
before the PR is promoted or merged. If the team's exit policy
requires a separate authenticated human reviewer, obtain that
review instead of treating a self-review as independent sign-off.
