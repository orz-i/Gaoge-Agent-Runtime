# PR #113 independent audit remediation — physical delegation admission and A2A provenance

## Frozen invariants

1. The first Store writer of the original `ItemStarted` is the **only** ordinary or recovered delegation entry authorized to attempt a Handoff physical start. Duplicate Started items, even after a successful `handoff_start_returned`, are unknown outcomes and never cause another `StartOrLoad` call. The original parent Tool handler and model are never replayed by recovery.
2. A generic Kernel `Create`/`Apply` can mint public-looking `a2a.remote` state, including Run IDs, Task IDs and terminal results. Such state is **not** a remote receipt. All locally accepted Shadow snapshots must carry an HMAC-SHA-256 lifecycle proof issued by the original A2A adapter after a real response/no-effect transition, bound to Run ID, Actor/Thread, RequestID, Goal, Run Kind, revision, status, state, result and error.
3. The first-party remote observer never performs remote network IO. Before accepting any current Run, including a terminal Run, `LoadRun` verifies the lifecycle proof. An unsealed state, changed identity, changed result, stale revision, or missing Task Context ID fails closed without creating a new failure or successful cancellation receipt.
4. The Host must supply an unpredictable **stable** `AttestationKey` of at least 32 bytes to `a2a.PluginDependencies` or `a2a.ShadowDependencies`. The secret stays outside Kernel/Timeline storage and outside untrusted Features. Recovery and recreated routing must reuse exactly the same secret. Uncontrolled key rotation or previously unsigned persisted shadows remain unresolved until explicitly reconciled; the SDK does not pretend their prior effect never happened.

## Trust boundaries and verification matrix

| Boundary | Adversarial scenario | Evidence / result |
| --- | --- | --- |
| Model/Host → Harness Tool | Repeated ordinary `Delegate`, `DelegateByExecutionRefID` | Same Started CAS; subsequent calls return unknown, physical call remains one |
| Store → physical ChildRunner | 24 simultaneous delegations to the same parent and goal | Exactly one physical Handoff call; tested under `-race` |
| Kernel writer → A2A remote observer | Forged same-Kernel, same-ID `completed` Task with forged Context | Rejected; no remote network IO |
| Kernel writer → A2A remote observer | Forged Task ID without Context, or forged Message ID | Rejected before terminal receipt |
| Kernel writer → previously attested remote Run | Copy valid Running signature and publish Completed with fabricated result | Rejected due to revision/status/result MAC mismatch |
| A2A response → first-party persisted shadow | Legitimate completed Task and reconstruct observer after restart | Accepted with same private key, no remote IO on read |
| Host key → recovered shadow | Missing, too-short or rotated secret | Rejected, never silently re-signed |

## Remaining release gates

- A real remote A2A provider acceptance test and physical Browser stop/ack protocol remain separate release requirements.
- Cross-process PostgreSQL adversarial admission and Host secret lifecycle/rotation procedures remain deployment-specific checks. This change introduces no migration or legacy proof bridge.

## Follow-up: original SendMessage uncertainty and complete public Control gates

- A `SendMessage` transport failure, incomplete Task/Context ID, malformed response, or unknown Task state is not authenticated remote failure/no-effect. `ShadowRunner.StartRun` preserves a **signed initial Running send intent**, the original Run ID and Message ID, and reports `ErrRemoteOutcomeUnknown` instead of signing terminal `failed`. A retrying Handoff never repeats the original physical Send; `LoadRun` with no bound Task ID reports an explicit unresolved identity and never issues an invented GET. An operator-proven remote outcome is required to reconcile this gap; there is no automatic resend or synthetic failed status.
- `Plugin.Cancel` and `Plugin.ResumeRun` now share the original `certifiedShadow` read-only proof check **before** a terminal or waiting-state shortcut. A same-Kernel forged `cancelled` Run cannot return a successful cancellation ACK. Missing/rotated proof or binding fails closed; a legitimate signed completed Run may be read without a remote side effect.
- The negative matrix tests response-lost, invalid response, missing Task ID, missing Context ID, unknown state, concurrent Handoff replay, same-ID unsigned terminal/waiting Shadow and wrong or rotated Host keys. All proof-bearing terminal statuses must originate from a validated, explicit remote Task/Message outcome.

## Manual terminal replay follow-up

- A fully authenticated original Handoff child may finish synchronously before a manual client receives the result. A repeat manual `Delegate` must not dispatch a second child but may return that **same** completed result read-only, after verifying the unique original started/terminal Items, the one settled `handoff_start_returned` physical grant, exact frozen ancestor Attempts, and physical local/A2A owner. A still-Running child, open grant, missing or contradictory proof remains `ErrExecutionOutcomeUnknown`. A live parent and a child that completed only once are tested end-to-end; no generic `StartOrLoad` replay is reintroduced.
