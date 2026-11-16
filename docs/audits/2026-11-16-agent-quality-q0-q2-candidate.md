# Agent quality Q0-Q2 candidate: baseline, experiment identity and navigation treatment

- Date: 2026-11-16
- Baseline: `main@db54e8e4db09020c3c0bd0bd7dccccc73b002d32`
- Scope: task-agent example and its offline launcher/report protocol only; not Kernel, Agent/Tool authorization, Provider dispatch, production Context or stored Run schema
- Status: **implementation candidate**, not a release or genuine-model quality improvement
- Operating rule: no paid Provider calls, untracked source probes or original raw reports are changed during this round

## Q0 — immutable observed baseline

The two original private JSON files were inspected **read-only** by the strict
`scripts/agent_quality_anchors.py --evidence-dir` verifier. The public
`benchmarks/agent-quality-anchors-v1.json` also passed its standalone check:
both of two anchored reports had matching SHA-256 and valid protocol/content-free
public facts. The original evidence remains untracked/operator-owned; this
candidate does **not** copy it, re-score it or write a derived raw report.

| Observation | Result | Limits |
| --- | --- | --- |
| 2026-11-07 complete 40-case, historical profile | 37 passed, 3 failed; 174 model calls, 137 Tool calls | Dirty-source earlier revision and one task family; NOT candidate-main performance |
| Distractor subgroup in that 40 | 5/8 passed; 28 Artifact reads for 8 tasks | Repeated broad/literal mismatched retrievals correlated with failures, not a proven unique cause |
| Earlier 2026-11-08 distractor-05 treatment | Real `agent.tool_limit`: 9 Archive reads, 0 drafts | Context counting passed; NOT a Context-budget failure |
| 2026-11-08 clean distractor-05 single pilot | 1/1 completed, 3 Tool calls and same Run/Turn recovery | New manifest/profile hash; 39 cases not sampled; not a full new success rate |
| Candidate baseline at `db54e8e4` | Default quality manifest `sha256:90a6b0b4505c6d29dafa39f26dac39589e78c905245f5a58a6a0404dea97bda2` | This is the CURRENT unmodified manifest, distinct from the 2026-11-08 pilot hash |

The correct initial classification is **strategy/evidence selection remains
unmeasured under today's complete profile**, not an established runtime Tool
defect. When a historical sample never reached a simulated crash boundary,
its absent recovery checks are not a failed recovery attempt. Likewise the
local `context_budget_exceeded` classification is not a Provider HTTP 4xx.

## Q1 — separate work from treatment without rewriting old datasetHash

The Go start event now emits:

- `datasetHash`: **unchanged** original complete JSON manifest hash (tasks,
  prompt, instructions, Tools, policies, Profile, Oracle); historical and
  normal `compare` use it exactly as before
- `workloadHash`: hash of task version, prompt, strict output schema, Oracle
  version, recovery requirement and ordered source-fixture hashes; excludes
  Provider configuration, instructions, Tool definitions and execution budgets
- `treatmentHash`: hash of exact instructions, Tool definitions, Tool policies
  and measurement/execution Profile, including hard budgets

Go JSON serialization of those frozen structures is deterministic. A Python
report reader validates that the **two new fields either occur together as
lower-case SHA-256 digests, or are both absent** on historical reports.
Reports remain schema v1; no backfill, invented zero or historical mutation.
These are self-declared experiment identities from the trusted runner, **not
signatures** of arbitrary external report contents; original raw SHA anchors,
clean source and report trust still matter.

`compare old new` intentionally remains strict on the full dataset hash.
`compare-treatment old new` is an explicit, *offline* observational comparison
that requires: exact same workload hash; same task/suite, profile, Provider,
endpoint, model, reasoning effort, output and context caps, repetitions and
unknown-model opt-in; and the **same exact clean source revision**. It rejects partial/missing
hashes, dirty source and mismatched budgets. It may show partial observations,
but never claims a full numeric uplift from incomplete/partial/pilot reports.
Different treatment hashes establish distinct declared inputs, **not proof
that only one variable changed**; code review must verify the intended change.

## Q2 — opt-in Host navigation A/B, not SDK policy

The **default** `quality` and `pilot-distractor` keep their prior text and
datasetHash. An explicit `qualityTreatment: generic-navigation-v1` opts into
a shorter, record-ID-based Host instruction: extract the exact target identifier
from the user's own task, avoid re-reading identical query/offset without
new evidence, use only authenticated original source after candidate hints,
and preserve one of the same ten Tool calls for required final `save_draft`.

The alternative does **not** change sample source facts, Task/Oracle, Tool
schema, immutable Tool/Model invocations, recoverable evidence, Provider route,
Context accounting, `MaxLLMCalls=12` or `MaxToolCalls=10`. Invalid, misspelled,
case-folded or legacy-suite treatment inputs are rejected. The frozen treatment
and full dataset hashes rotate; workload hash stays the same.

For operator-controlled *future* paid sampling, use the existing interactive
baseline `quality`/ `pilot-distractor` commands as control and the opt-in
`quality-generic` / `pilot-distractor-generic` as treatment. The interactive
launcher still builds first and asks for a hidden key; pilots still run at most
one case. To compare two **complete**, clean, otherwise matched 40-case reports
offline:

```sh
python3 scripts/run-agent-baseline.py compare-treatment \
  artifacts/agent-baselines/control.json \
  artifacts/agent-baselines/generic.json
```

The old `compare` deliberately rejects the different dataset hashes.
Do not call `compare-treatment` a randomized trial, statistically significant
result, or proof of more general Agent intelligence. Use at least 2–3 balanced
repetitions and a held-out corpus before claiming a production uplift.

## Verification recorded on this local worktree

- Public and private historical source anchor verification: **2/2 passed** (read-only)
- `make agent-baseline-test`: **137 Python tests passed**, including strict
  history handling and A/B negative comparisons
- `go test ./go/agent-runtime-harness/examples/task-agent -count=1`: **passed**;
  alternative instruction path passes 40/40 source-blind synthetic Agent tasks
  and existing default full task-agent tests
- Targeted `go test -race -count=1` on treatment, recovery and original
  manifest identity: **passed**
- `make agent-quality-calibrate`: **40/40 scripted**, real Agent/Tool/Context
  mechanics only, no Provider reasoning or paid inference
- `metadata`, `fmt-check`, `tidy-check`, `integration-compile`, full
  workspace `go-vet` and `go-lint`: **passed**
- TypeScript lint, typecheck, 46 tests and build: **passed**; evaluation,
  benchmark and OTel smoke, A2A product and clean-consumer release: **passed**
- Whole-workspace `go-test` attempt: **incomplete** after Core/Provider/Harness
  tests passed; remote Mac Pi request ended during task-agent module execution
  with no assertion failure in the returned log. The task-agent module was
  subsequently tested independently and passed. Additional Harness-Postgres,
  MCP, OTel and A2A module tests completed separately and passed. Remaining
  whole-workspace Go test modules and full workspace race/security gates
  are **not certified** by this evidence.

## Bounded self-review follow-up (2026-11-16)

Self-review walked Host input → Go manifest hash creation → Python start event
validation → read-only treatment comparison. Two reproducible report-contract
issues were closed without changing SDK execution behavior:

1. Same-treatment reports previously returned eligible from
   `compare-treatment`, despite not forming an A/B strategy comparison.
   The experimental comparator now requires distinct `treatmentHash` values;
   ordinary `compare` remains the appropriate same-treatment repeat check.
2. New explicit `generic-navigation-v1` reports previously could omit **both**
   experiment identity hashes and be accepted as historical-format reports.
   Explicit treatment now requires both validated hashes; historical reports
   without a treatment remain readable and untouched.

Each case was reproduced against the prior implementation and rejected by the
new regression test. The 137-test Python protocol suite passed after repair.
This is a scoped self-review, **not** an external independent audit, complete
whole-workspace CI admission or real-model effectiveness evidence. No further
execution-authorization or Kernel changes were indicated by this review.

## PR #114 audit follow-up — F1 report attribution

The independent PR audit reproduced a report-pair inconsistency: two validated
reports with the **same** recorded `qualityTreatment` label were accepted by
`compare-treatment` after only their self-declared `datasetHash` and
`treatmentHash` values were changed to distinct, well-formed hashes. This was
an A/B attribution risk, not a Runtime or Provider authorization issue.

The comparator now requires **both** distinct recorded Host treatment labels
(the default and the explicitly opted-in `generic-navigation-v1`) and distinct
`treatmentHash` values. Its table displays the labels alongside the existing
hash comparison. The negative regression was observed failing before the fix;
post-fix tests cover both same-label spoofing variants and both valid A/B
orders. All original workload/source/profile/budget checks remain intact.
The hashes remain runner-declared identities, not cryptographic signatures.

## Conformance and release gates

The new Go tests assert the current default manifest hash, separate identities
and invalid Host input; an opt-in synthetic 40-case pass exercises real local
Agent/Tool/Context/fault/recovery mechanics for each sample, without real-model
inference. Python tests include legacy report reload, wrong/missing hash pairs,
strategy spoofing, matched and mismatched comparison eligibility, and no
Provider launch in the read-only compare command.

This gate **cannot certify** physical remote Provider behavior, the quality of
a real model's retrieval decisions, external billing, independent code review,
or a new complete clean-source 40-case success rate. A scripted 40/40 verifies
only mechanics. Future Q3 Context changes require separately diagnosed budget
pressure and a new approval, not a speculative Kernel or replay rewrite.

Useful read-only/local checks:

```sh
make agent-quality-anchor-check
make agent-baseline-test
go test ./go/agent-runtime-harness/examples/task-agent -count=1
make agent-quality-calibrate
git diff --check
```

Additional full CI/race/security/real-engine gates and any actual paid A/B
sampling must be recorded against a named candidate commit before merge.
