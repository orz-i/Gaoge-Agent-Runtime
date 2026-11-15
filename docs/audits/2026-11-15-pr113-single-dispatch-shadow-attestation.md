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

## Final follow-up: resumed input SendMessage also requires single-dispatch

Independent review reproduced a P1 variant in `ShadowRunner.ResumeRun`: after the remote accepted an input message but the HTTP reply was lost, the earlier `resumeSendFailed` returned the locally signed Run to `waiting_input`. A second `ResumeRun` then sent **another physical message** (same MessageID), relying on an unsupported remote idempotency guarantee. The old smoke scenario explicitly rewarded this unsafe retry. A separate P2 variant forwarded the raw `RemoteClient.SendMessage` transport error string, potentially exposing credentials to callers.

- Persist a signed `PendingMessageID` / input digest and transition to `running` **before** the one physical resume Send. No transport failure, response parse failure, process restart, competing caller, or same-state `GetTask` may issue that message again or grant another resume. `MessageID` is only correlation identity; it does not authenticate a peer-side deduplication guarantee.
- On a transport error, return only the stable `ErrRemoteOutcomeUnknown`, keeping the original authenticated `running` state and not exposing upstream error text or creating a fabricated failure/no-effect receipt. A `GetTask` returning a nonterminal status while a resume remains pending is insufficient to prove the input was not consumed: the observer reports unknown, does not reopen `waiting_input`, and never replays Send.
- A *bound, authenticated* later terminal `GetTask` may settle the original Run read-only. A verified cancellation of that known original Task remains permitted; no alternate Task/Context may be substituted. If the peer never produces a definitive terminal status, reconciliation requires an operator/provider-backed decision. No automatic replay or unverified pending-clear path is introduced.
- Regression matrix: lost response / private transport error, malformed or wrong-identity initial input reply, repeated same/different input, stable-key runner reconstruction, concurrent physical sends, same-state GET, eventual terminal GET, and cancel during uncertain input. Updated the A2A deterministic smoke dataset and baseline to assert **one physical resume** and read-only terminal reconciliation instead of rewarding a retry.

## SQLite `-race` CI conformance fixture follow-up

- On Head `43a591798f497723357581ddba324a8ac227333b`, the full GitHub Go race job reached the SQLite `TestEA1ChildAgentCancelPhasesNativeSQLite/P2/explicit-native/tool` fault vector but intermittently timed out its *successful* fixture Model callback before the injected `tool.completed` Store fault. The callback reads the real SQLite Store while the test-only Model timeout was only 100ms. The resulting `ErrModelTimeout` plus unsettled child Grant was a different cutpoint, not evidence that the intended fault failed to fence an actual execution. Ordinary unit/coverage and A2A Race tests passed.
- Stabilized only the EA1 child-cancellation Conformance fixture (no production cancellation/Store semantics changed): use a 900ms Model/Tool call window, strictly inside the explicitly configured 2-second native lease minus its required 1-second renewal safety margin. The test now observes and waits for the latest real same-epoch Lease expiry if the Agent legitimately renewed during a slow callback; waiting only for the obsolete initial expiry incorrectly asserted that the execution token was revived.
- Added a deterministic 175ms simulated delayed Model Store read to the exact P2 explicit-native/tool vector. It must reach and verify the original `tool.completed` Store fault, retain its original physical Grant and origin proof, and preserve the former cancellation/restart/expiry expectations. The unchanged 100ms timeout would instead reject the callback. The same complete test matrix is exercised on Memory, SQLite and GORM; the true source fault and all cleanup fences remain asserted.

## Final governance: A2A external Control Admission — Revision / effect / ACK unified

The final independent review of Head `86ba0210` found three deterministic P1 violations: (1) a delayed `ResumeRun` could issue `SendMessage` **after** `CancelRun` had returned a terminal cancellation ACK, (2) a public `Plugin.ResumeRun(expectedRevision=R)` paused during binding resolution could send a stale response to a new wait at revision `R+2`, and (3) public `Plugin.Cancel` could use the same stale revision to cancel the newer wait. The old implementation fenced the *initial control intent* but allowed a concurrent remote GET/Cancel or new Control to certify a different physical effect before that original callback had actually returned.

**Frozen execution protocol and security invariants:**

1. **One shared atomic authority:** public `ResumeRun` and `Cancel` carry the original requested Kernel revision through Host binding resolution to the same signed `Kernel.Apply` revision CAS immediately before physical IO. A conflicting CAS or reloaded revision rejects the control without invoking `RemoteClient`. Direct `ShadowRunner` controls select an implicit current revision but obey the same CAS.
2. **Physical intent must close, not merely begin:** the original A2A adapter persists `PendingMessageID` or `PendingCancel` at admission (`running`) before invoking `SendMessage` or `CancelTask`. A second Control, observer or cancellation call on any Runtime sharing that Store cannot silently supersede an unfinished intent. The *original physical callback* must actually return and commit an HMAC-bound `PendingMessageReturned` / `PendingCancelReturned` marker before `LoadRun` may use a remote terminal GET to settle the Run. A physical effect may still happen between committed admission and network invocation; therefore a terminal GET, including `TASK_STATE_CANCELED`, is not an ACK while the callback-return fact is absent.
3. **No implicit replay:** lost HTTP replies, malformed or foreign Task/Message responses, concurrent calls, process restarts and ambiguous Store write acknowledgements never retry the original physical Send/Cancel. Identical Message IDs are not a remote deduplication protocol. Once a callback has returned, an independently verified original Task terminal may be read and sealed; a nonterminal Task observation remains outcome-unknown while a submitted control is unresolved. A known Task may receive a new cancellation intent only after its previous resume callback has returned, and the intent must pass its own CAS.
4. **Fail closed under data and identity faults:** the MAC still authenticates the Run, Task/Context identity, exact remote discovery revision, new pending/returned control flags and terminal result. A generic Kernel terminal twin, changed/foreign Task, stale expected revision, missing key or unsigned state never authorizes remote physical IO or a successful ACK. Provider errors from `SendMessage`, `CancelTask` and `GetTask` never appear verbatim in SDK Control/observer errors.
5. **Crash limit is explicit:** if the Host crashes after committing an intent but before the original callback-return marker is durably committed, the physical effect can be unknown indefinitely. No lease expiration, time threshold, process-local lock or remote status guess revives that authority. A future operator/remote-provider-backed reconciliation protocol is required before the result can be safely closed. This is intentionally a breaking behavior hard cut, not a migration shim.

**Verification matrix on this change:**

| Admission race / abnormal transition | Required invariant | Verification |
| --- | --- | --- |
| Resume CAS committed; original process suspended before `SendMessage`; independent Runtime attempts Cancel/GET | No Cancel ACK, no remote GET, no additional physical IO until callback return | `TestPR113ControlResumeIntentFencesCancelACKAcrossRuntime` |
| Cancel CAS committed; original process suspended before `CancelTask`; separate Control resumes input | No second Send or premature terminal GET | `TestPR113ControlCancelIntentFencesResumeAndObserver` |
| Binding resolver pauses public Cancel or Resume after preflight; another observer commits a new wait | Original `expectedRevision` stays frozen; zero physical stale controls | `TestPR113ControlStaleRevisionsDoNotReachRemote` |
| 24 simultaneous public Resume/Cancel requests | At most one physical effect through Store CAS | `TestPR113ControlConcurrentResumeCancelCASGrantsOnePhysicalEffect` |
| Store intent or callback-return CAS commits but original caller loses the Store ACK | Unverified effect remains signed and read-only; no new physical dispatch | `TestPR113ControlStoreCommittedLostACKPreservesPhysicalFence` |
| Remote GET transport error contains provider credentials | No leaked upstream error text, no successful Cancel | `TestPR113ControlReadFailureDoesNotLeakProviderErrorOrCancel` |
| Two independent SQL connections with one suspended writer, Resume then Cancel / Cancel then Resume | Same atomic admission in SQLite and real PostgreSQL, not just MemoryStore locks | `TestRealPostgresAndSQLiteA2AControlAdmissionAcrossConnections` (real-engine integration module) |

The deterministic A2A evaluation dataset is incremented to revision 3 and now also requires the signed callback-return event. Remaining external release gates include actual remote A2A service compatibility, A2A Host key custody/rotation runbooks and physical Browser stop ACK. CI for the final pushed Head must still pass before merge.
