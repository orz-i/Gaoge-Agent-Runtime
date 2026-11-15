# PR #112 — A2A Shadow remote identity P1 follow-up

- Date: 2026-11-14
- Scope: `go/agent-runtime-a2a/shadow_runner.go`; existing SubAgent/Handoff-facing Shadow Runner
- Finding: **P1 safety and data-integrity blocker**, reproducible before the patch
- Target: `refactor/subagent-only-orchestration`, PR #112; final PR Head CI and merge review remain separate gates

## Security invariants and trust boundary

A remote A2A client response is untrusted relative to the persisted Shadow Run.
The first Task result establishes a Task ID **and** Context ID; neither
`GetTask`, `CancelTask`, nor a later `SendMessage` may substitute another pair.
The same-state fast path is not allowed to skip identity validation. A Task's
status/history messages with explicit IDs, and its artifacts, cannot claim a
different task/context. A Message replying to an already-bound Task must carry
both matching identifiers. A valid initial direct Message has no prior Task
binding and remains supported.

Rejection must occur **before** completing/cancelling or persisting new remote
identity, results, prompts or artifacts. In a resume whose send intent was
already durably written, a rejected response leaves the original Task/Context
binding and pending message intact; externally unknown send effects are not
replayed or reported as success. Later read-only `GetTask` of the original
identity may still reconcile a legitimate terminal outcome.

## Reproduced variants and fixes

| Path | Adversarial response | Original behavior | Fixed behavior |
| --- | --- | --- | --- |
| `LoadRun` / `GetTask` | Same nonterminal state, foreign Task ID or Context ID | Returned nil without checking | `ErrRemoteBindingChanged`; original complete snapshot preserved |
| `LoadRun` / `GetTask` | Foreign identity and different state including terminal | Accepted foreign state/result | Rejected before `applyTask` mutations |
| `CancelRun` | `GetTask` or `CancelTask` reports foreign identity | Could issue cancellation or accept alien terminal result | Reject before cancel dispatch or terminal projection |
| `ResumeRun` / `SendMessage` | Task response with foreign ID or Context ID | Overwrote original binding; could complete | Fail closed; original pending message retained |
| `ResumeRun` / `SendMessage` | Message with foreign/missing Task ID or Context ID | Overwrote context and completed | Reject without settlement, preserve original pending message |
| Task nested payload | Foreign status message, history message or artifact identity | Could project alien evidence | Reject the entire Task before any projection |
| Invalid interaction union | Both Task and Message returned | Task silently preferred | Invalid result; no terminalization |
| Positive compatibility | Matching bound IDs, optional nested IDs, first unbound direct Message | Existing valid path | Still supported |

## Verification

- Added table-driven regression matrix `shadow_identity_test.go`. All negative
  cases reproduced on the original code before modification; they pass after
  the patch. Tests compare persisted snapshots for read/cancel rejection and
  assert no success result or identity mutation after resume mismatch.
- `go test -count=1 ./go/agent-runtime-a2a/...` — passed.
- `go test -race -count=1 ./go/agent-runtime-a2a/...` — passed.
- `go vet ./go/agent-runtime-a2a/...` — passed.
- `make go-lint` (all modules) and `make a2a-product-check` — passed.

## Rollout and remaining verification

This is a fail-closed tightening for A2A responses from untrusted/misrouted
remote agents. In particular, a bound Task's previously tolerated Task-ID-less
Message now fails instead of providing unauthenticated completion. No old
identity-rebinding compatibility path is retained. A fresh direct Message is
not affected. Full PR Head CI and independent merge admission must be evaluated
after pushing this remediation commit; no production rollout was performed.
