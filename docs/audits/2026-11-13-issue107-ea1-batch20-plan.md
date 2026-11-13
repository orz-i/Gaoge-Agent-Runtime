# EA-1 batch 20: fixed Harness AgentTask cells

Scope: EA1-H-AC-P0 through P5 only. B19 remains signed at 126 PASS / 18 UNRUN; this plan is not acceptance evidence. No production change, push, PR or merge is intended. H-FROZEN and Hosted MCP transport remain separate WP-GAOGE blockers.

## Closed entry and backend checklist

Exercise the actual Harness Workflow → AgentTask → approval/cancellation path, with the original static executor, Agent runner, relation recorder and effective persisted configuration. Do not substitute a bare Kernel call or an adjacent matrix cell. Add test-only Kernel factories for Memory, native SQLite and native PostgreSQL to the Harness PostgreSQL test module. PostgreSQL competitors use independent pools. SQLite recovery closes every test-owned handle before reopening. Memory explicitly retains state. Host persistence recovery uses real PostgreSQL; distinguish Host and Kernel persistence in each result.

Use exact public admission order and error contracts: task/checkpoint and Turn checks precede physical admission; relation/origin verification precedes cancellation election and budget changes. The trusted static verifier binds the real frozen Effect input to the concrete child; a falsely reporting trusted verifier is not claimed to be detected. All rejected inputs preserve full original child/ancestor snapshots, Host Invocation, timeline/election, grant and budget except an explicitly authorized original CancelRun transition.

## Controlled phases

- P0: original caller, task/checkpoint, relation, source and runner rejection before physical work. Complete before/after state comparisons, not merely zero dispatch counters.
- P1: actual Harness approval admission held before dispatch. Competing self/ancestor control and writes cannot revoke original authority; callback lifetime remains authoritative.
- P2: a real Tool send with missing outcome, followed by fresh-owner recovery. Original grant, intent and budget remain; no inferred completion or TTL replay. A merely blocked callback is not sufficient unknown-outcome evidence.
- P3: original complete Tool request/result and receipt are durable before the actual child Mark. Reconcile cannot synthesize Mark. A verified terminal read-only cancellation confirmation is distinguished from grant retirement and must not issue control.
- P4 / G11: busy child → original CancelSubtask election and authorized CancelRun → actual Tool receipt and original settlement → retry same task under the original uncompleted cancellation intent → completion Append commits but ACK is lost → reopen Host and Kernel → original receipt, budget and no replay. Do not expect a Running child with no settlement to make cancellation succeed.
- P5: deterministic cancellation-first and approval-first orderings with barriers. Preserve the winning authority, exact checkpoint and original budget election. No random acceptable-error union. No compensation claim for a Workflow with no compensation definition.

## Freeze and evidence gates

Before source freeze, statically check the entire fixture: real default/native ports, complete interface forwarding, mandatory preflight, actual fault hit count, valid StartRequest, persisted/effective baselines, original request tuple before consumed-field pruning, full response/result/checkpoint equality and exact typed errors. No speculative representation normalization: only proven equivalent empty representation or same time instant may be normalized on comparison copies, with all remaining fields compared.

Freeze one source SHA; run minimal actual-target smoke before the cross-Store expansion. Preserve any failed source/log and classify it before a bounded correction. Run final directed race evidence against the same source, obtain independent bounded source and durable evidence review, then change only these six ledger rows. Old rows keep their true historical SHAs until the final common-SHA execution. The final Runtime matrix run and mandatory local gates remain required; no batch result certifies overall release GO.
