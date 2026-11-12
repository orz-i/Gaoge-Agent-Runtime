# Issue #107 — Fixed Execution Acceptance Matrix (EA-1)

Status: **matrix inventory locked for WP-RUNTIME; no cell certified yet**.
Specification: [EA-1 contract](issue107-execution-authorization-contract.md).
Baseline: Runtime `c17eeb49cc1d95b39f16c796edcd403d9b0a5ec2`, Gaoge `c7759dcb27d9ef193f5079be0473cff61626676a`.
Issue: [#107](https://github.com/orz-i/Gaoge-Agent-Runtime/issues/107); Host [draft PR #69](https://github.com/orz-i/Gaoge/pull/69).

## Legend — intentionally not a pass/fail assertion

Every matrix cell identifies a **concrete test anchor or a named missing test**; the executing implementation entrypoint is on the left.

- `E##`: source test already exists (see registry). **Not yet a verified PASS for this exact phase**; the reviewer must confirm its actual assertion and record the frozen SHA/result.
- `G##`: named coverage gap (test to implement or provide precise technical non-applicability before release). All applicable G cells block WP-RUNTIME acceptance.
- `B##`: known external/integration blocker with a failing test; it cannot be reclassified as a Runtime PASS.
- `N1`: control/reconciliation has no external physical dispatch; still must verify no physical side effect and original settlement.
- `N2`: physical dispatch belongs to the linked Model/Tool/Effect owner and must be assessed under that row rather than repeated in recovery.

The same E identifier across several columns **does not automatically prove several columns**. In the frozen acceptance ledger each cell gets its own `PASS / FAIL / UNRUN / N-A-approved` verdict, evidence path, command, SHA, backend and expected error/receipt. An E that does not actually assert the stated phase becomes an OPEN gap; there is no inferred green status.

## Timing axes (physical effect state, not merely RunStatus)

| Phase | Required controlled cut point |
| --- | --- |
| **P0** | Before Store admission: stale revision, duplicate Run/Attempt, invalid actor/parent/origin, foreign runner, failed preflight |
| **P1** | Granted but before physical dispatch: competing self/ancestor Apply, Cancel, approval, lease revocation and callback lifetime |
| **P2** | Physical send may have occurred; result missing/timeout/ambiguous, or pending remote callback |
| **P3** | Original receipt/no-effect proof durably committed but Grant not yet retired |
| **P4** | Settlement marker committed, ACK lost, new process/Store instance and recovery |
| **P5** | Competing Cancel, compensation, retry, resume, deadline and shared-budget election |

## Fixed entrypoint × Root/Child × timing coverage

| ID / feature and real entrypoint | P0 | P1 | P2 | P3 | P4 | P5 |
| --- | --- | --- | --- | --- | --- | --- |
| AR-S · Root Agent `agent.Runner.StartRun` | E05 | G01 | G02 | G02 | G03 | G04 |
| AR-R · Root Agent `Runner.Resume` | G05 | G01 | E16 | E23 | G03 | G04 |
| AR-A · Root Agent `ResolveApproval/ResumeToolWait` | E17 | G07 | G08 | E17 | G03 | G04 |
| AR-C · Root Agent `kernel.Runtime.Cancel` / Harness | G09 | G09 | N1 | G09 | G15 | G09 |
| AR-Q · Root Agent Model/Tool recovery and `ExecutionLease` | E23 | N2 | E16 | E23 | G03 | G10 |
| AC-S · Child Agent `Runner.StartRun` | E13 | G05 | G05 | E12 | E12 | E14 |
| AC-R · Child Agent `Runner.Resume` | E04 | E03 | E16 | G06 | E12 | E14 |
| AC-A · Child Agent `ResolveApproval` | E13 | E13 | E17 | E13 | G07 | E15 |
| AC-C · Child Agent `Runtime.Cancel` / Harness `CancelSubtask` | E14 | E03 | E03 | E15 | E15 | E15 |
| AC-Q · Child Agent `ReconcileChildPhysicalGrant` | E12 | N2 | E12 | E12 | E12 | E15 |
| WR-S · Root Workflow `workflow.Runner.StartRun` | E01 | E01 | E06 | E01 | E10 | E01 |
| WR-R · Root Workflow `Runner.Resume` | E04 | E09 | E06 | E01 | G09 | E09 |
| WR-W · Root Workflow `ResolveWait` | E08 | E08 | G17 | E08 | G09 | E08 |
| WR-C · Root Workflow `Cancel` + compensation | E07 | E07 | G08 | G08 | G08 | E07 |
| WR-Q · Root Workflow `ReconcileChildPhysicalGrant` | E10 | N2 | G17 | E10 | E10 | G09 |
| WC-S · Child Workflow `Runner.StartRun` / subworkflow | E02 | E02 | E06 | E02 | E11 | E02 |
| WC-R · Child Workflow `Runner.Resume` | E04 | E02 | E06 | E02 | G09 | E02 |
| WC-W · Child Workflow `ResolveWait` | E08 | E02 | G17 | E02 | G09 | E08 |
| WC-C · Child Workflow `Cancel` + compensation | E07 | E07 | G08 | G08 | G08 | E07 |
| WC-Q · Child Workflow `ReconcileChildPhysicalGrant` | E11 | N2 | G17 | E11 | E11 | G09 |
| H-AC · Harness Workflow AgentTask proof, approve, cancel | E13 | E13 | E17 | E13 | G11 | E15 |
| MW-NEXT · `plugin.RunChain/ModelChain/ToolChain.Invoke` callback lifetime | E18 | E18 | G02 | G02 | G03 | G04 |
| H-FROZEN · Host original frozen Actor/Thread/Context/Tool/limits/Attempt | E21 | E22 | G12 | E21 | G12 | E14 |
| S-CTL · Store self/ancestry Apply, ApplyControl, Cancel | E04 | E03 | E03 | E03 | E19 | E03 |

`AR` = Agent Root, `AC` = Agent Child, `WR` = Workflow Root, `WC` = Workflow Child. A `Cancel` on an Agent is a Kernel/Harness entrypoint, not an invented public `agent.Runner.Cancel`.

### Existing evidence registry (anchored to actual source names)

| Ref | Test / suite source | Primary assertion |
| --- | --- | --- |
| E01 | `go/agent-runtime/conformance/root_workflow_execution.go::RunRootWorkflowSuite` | Root Workflow own Grant, concurrency, cancellation, unknown Pending |
| E02 | `go/agent-runtime/conformance/workflow_child_execution.go::RunParentedWorkflowSuite` | Parented Workflow Effect, pending, Wait/Cancel |
| E03 | `go/agent-runtime/conformance/issue107_seventh_child_self_fence.go::RunAuditPhysicalChildSelfFence` | Executing Run self-fence, parent fence, rejected Cancel preserves Agent lease; exercised by Memory/GORM/SQLite/native PG tests |
| E04 | `go/agent-runtime-postgres/issue107_sixth_revision_audit_real_test.go::TestAuditIssue107PostgresBeginCannotBindCallerExpectedChildRevision` | Real PG stale revision and expected admission |
| E05 | `go/agent-runtime/agent/issue107_seventh_root_agent_start_test.go::TestAuditIssue107RootAgentStartMiddlewareExecutesBeforeFailedCreate` | Duplicate Root ID does not run middleware before Kernel Create |
| E06 | `go/agent-runtime/workflow/issue107_seventh_attempt_noeffect_test.go::TestAuditIssue107NoEffectFromPriorAttemptSettlesUnknownLaterDispatch` | Attempt-scoped no-effect, unknown side effect must not retry |
| E07 | `go/agent-runtime/workflow/issue107_seventh_cancel_compensation_test.go::TestAuditIssue107CancelCompensationBypassesRootPhysicalGrant` | Cancel compensation physical admission |
| E08 | `go/agent-runtime/workflow/issue107_sixth_audit_regression_test.go::TestAuditIssue107StaleWaitCASPinsNoEffectAncestorGrant` | Stale Wait control must not mint an unknown Grant |
| E09 | `go/agent-runtime/workflow/issue107_root_concurrent_audit_test.go::TestAuditIssue107RootWorkflowConcurrentResumeDoubleDispatchesExternalEffect` | Root duplicate queue-work dispatch denied |
| E10 | `go/agent-runtime-sqlite/issue107_root_ack_recovery_test.go::TestIssue107SQLiteRootWorkflowOriginalMarkAckLostReopenRecovery` | Root Workflow marker ACK lost and SQLite reopen recovery |
| E11 | `go/agent-runtime/workflow/issue107_workflow_ack_audit_test.go::TestAuditIssue107WorkflowCompletedMarkAckLostCannotReconcileParent` | Child Workflow original Mark ACK lost recovery |
| E12 | `go/agent-runtime/agent/issue107_child_grant_recovery_test.go::TestIssue107ManualGrantRecoveryOnlyAfterPhysicalStateSettles` | First-party Agent reconciliation only when physical effects settled |
| E13 | `go/agent-runtime-harness/issue107_workflow_agent_task_approval_test.go::TestIssue107WorkflowAgentTaskSubtaskApprovalAndResume` | Original Workflow AgentTask child approval and Tool continuation |
| E14 | `go/agent-runtime-harness/issue107_independent_audit_regression_test.go::TestFinalAuditParentTerminalCannotApproveLiveChild` | Terminal parent rejects new child approval |
| E15 | `go/agent-runtime-harness/issue107_workflow_agent_task_approval_test.go::TestIssue107AgentTaskCancelUsesExactPhysicalFence` and Gaoge `TestIssue107HostedParentCancelCanRetryAfterBusyPhysicalTool` | Original cancellation and busy Tool resolution |
| E16 | `go/agent-runtime/agent/execution_policy_test.go::TestToolPolicyTimeoutDoesNotRetryAmbiguousSideEffect` | Ambiguous Tool timeout cannot auto retry |
| E17 | `go/agent-runtime/agent/agent_test.go::TestDeferredToolApprovalRequiresExplicitResume` | Persisted approval before Tool Resume |
| E18 | `go/agent-runtime/plugin/issue107_async_next_lifecycle_test.go::TestIssue107MiddlewareCannotInvokeRetainedNextAfterPhysicalCallEnds` | Middleware Next cannot escape physical callback |
| E19 | `go/agent-runtime-postgres/issue107_child_receipt_ack_loss_real_test.go::TestIssue107RealPostgresPhysicalSettlementAckLostAcrossStoreInstances` | Real PG durable original receipt and ACK-loss recovery |
| E20 | `go/agent-runtime/agent/model_invocation_fault_test.go::TestModelInvocationRetryableProviderErrorReusesPendingIntent` | Original ModelInvocation retention and durable attempt |
| E21 | Gaoge `backend/internal/bootstrap/mainapi/issue107_independent_audit_regression_test.go::TestFinalAuditFrozenAgentLimitsCannotBeUpgradedBySameIDChild` | Frozen execution budget source |
| E22 | Gaoge `backend/internal/bootstrap/mainapi/agentruntime_issue107_cancel_followup_test.go::TestIssue107ActualHostedRejectsDirectApprovalWithoutPhysicalGrant` | Host bypass prevention |
| E23 | `go/agent-runtime/agent/model_completion_authority_test.go::TestModelCompletionAuthoritySourceAndReplay` | Root Agent Model source/replay authority |

### Named gaps (fixed targets, not license for unrelated new requirements)

| Ref | Required assertion / suggested test name | Scope and expected evidence |
| --- | --- | --- |
| G01 | `TestEA1RootAgentMiddlewareAdmissionAndExecutionLease` | Before any Root middleware physical effect, original Root has authoritative owner/lease; independent competing Cancel fails without revoking it |
| G02 | `TestEA1RootAgentMiddlewareUnknownEffectCannotBeDeclaredSettled` | Root Host callback returns error after synthetic external send, no fabricated no-effect/committed result |
| G03 | `TestEA1RootAgentProviderReceiptAckLossAcrossRestart` | Original provider/tool receipt persisted; new worker recovers without new external call |
| G04 | `TestEA1RootAgentCancelAndHostMiddlewarePhysicalOrdering` | Root Cancel/Deadline vs active middleware/Model/Tool dispatch across all Stores |
| G05 | `TestEA1AgentChildStartFailureBeforeAndAfterGrant` | Start preflight, wrong relation, pre-Grant conflict, post-Grant unknown callback separated |
| G06 | `TestEA1AgentChildResumeReceiptAndRevisionMatrix` | Exact revision admission, same Tool result and future attempt attempts bound |
| G07 | `TestEA1ChildApprovalAckLossAndReplay` | Approval consumed/Tool dispatch with lost ACK, restart and mismatched checkpoint |
| G08 | `TestEA1WorkflowCompensationUnknownAndRecoveredReceipt` | Root & child compensation send unknown, receipt committed, lost ACK, persisted recovery |
| G09 | `TestEA1WorkflowResumeAckLossAfterWaitCheckpoint` | Root & child interrupted Wait/Resume with no physical work vs unknown send |
| G10 | `TestEA1RootAgentLeaseOwnerTakeoverAndReceiptIdentity` | Old execution epoch cannot complete an operation after legitimate takeover |
| G11 | `TestEA1HarnessCancelledBudgetSameIntentAcrossStoreRestart` | Busy child, original cancellation election, no budget reset or new side effect after restart |
| G12 | `TestEA1FrozenOriginAndAttemptTamperStateMachine` | Actor, tenant, thread, parent chain, Run ID, attempt, Model/Tool route and duplicate relation mutation variants |
| G15 | `TestEA1RootAgentControlBeforeAndAfterLeaseAdmission` | Direct Control/Cancel, no partial revocation or hidden epoch advance |
| G17 | `TestEA1WorkflowInterruptedCallNoReceiptVersusPreDispatch` | Crash exactly before send, after send before receipt, after receipt before Mark; distinguish outcomes across stores |

A gap can be closed by extending an existing named test instead of adding a new file; the reviewer records the exact replacement assertion and path. A test with a misleading historical `TestAudit...` name is not exempt from proof.

## Store conformance (same state machine, not three bespoke checklists)

| Contract dimension | Memory | SQLite (native) | PostgreSQL (GORM + real server) |
| --- | --- | --- | --- |
| Root Workflow owner, stale revision, duplicate Resume | E01, E09 | E01, E09 | E01, E04, E09 |
| Parented Workflow owner and terminal ancestor | E02 | E02 | E02, E04 |
| Executing child self / ancestor fence, original lease preserved | E03 | E03 | E03 native real PG |
| Workflow Attempt/NoEffect provenance | E06 | E06 via Workflow suite; **G12 for persisted restart matrix** | E06 via suite; **G12 for cross-connection matrix** |
| Original settlement marker and lost ACK | E11 | E10, E11 | E19; **G08 for compensations** |
| Root Agent owner/ExecutionLease + Host middleware | G01 | G01 | G01 |
| Stale reads, concurrent Cancel/Apply/Approval | E03, E08 | E03, E08 | E04, E03; require real two-connection timing |
| Fault injection, corrupt/foreign state/attempt, state machine | G12, G17 | G12, G17 | G12, G17 |

Conformance must assert typed error **and** no mutation/no physical dispatch, original Grant+lease unchanged, no duplicate receipt and successful original owner continuation. `-race` is supplementary; deterministic lock/interleaving tests are mandatory.

## Runtime command gates (run at the SAME frozen source commit)

The read-only inventory check is `python3 scripts/check-issue107-matrix.py`; its `--strict` mode must remain NO-GO until **all 144 cells** are recorded on the same frozen SHA. Do not enable `--strict` as a required CI check while the initial ledger still says `UNRUN` for every cell.

```bash
go -C go/agent-runtime test -count=1 ./...
go -C go/agent-runtime-harness test -count=1 ./...
go -C go/agent-runtime-postgres test -count=1 ./...
go -C go/agent-runtime-sqlite test -count=1 ./...

go -C go/agent-runtime test -race -count=1 ./...
go -C go/agent-runtime-harness test -race -count=1 ./...
go -C go/agent-runtime-postgres test -race -count=1 ./...
go -C go/agent-runtime-sqlite test -race -count=1 ./...

# Use a dedicated, drained, disposable real PostgreSQL instance; NO production DSN.
TEST_POSTGRES_DSN='<isolated-DSN>' go -C go/agent-runtime-postgres test -race -count=1 ./... -run 'Issue107|TestAuditIssue107|TestEA1'

# Also run module-specific golangci-lint, external Go consumer contracts,
# node scripts/check-beta.mjs, repo-defined mandatory CI and persisted
# restart/ACK-loss tests. Capture exact command, SHA, exit code and logs.
```

## Two-work-package exit

**WP-RUNTIME GO:** EA-01..EA-11 true; all applicable matrix cells proven with an exact named test, assertion, backend and commit; all G gaps closed or explicitly N/A-reviewed; real Postgres state machine; complete module, Race, lint, consumer/beta, original child approval and required GitHub PR-head checks; no unresolved P0/P1. No unsafe partial merge.

**WP-GAOGE GO:** all WP-RUNTIME public contracts and commit pinned; original full MCP Workflow AgentTask approval/Tool/receipt scenario passes; frozen Model/Tool/Context/budget source and provider replay correct; exact Gaoge PR-head CI complete; no unknown provider replay hidden behind Runtime FAIL-CLOSED.

For a closed matrix, a future review is **one targeted final acceptance review of the same invariant set**, not another unbounded architectural audit. A newly demonstrated P0/P1 is still a blocker and must be assigned to its existing cell or a reviewed scope amendment.
