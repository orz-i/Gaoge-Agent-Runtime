# 2026-11-05 — Session read authority convergence (governance round 2)

Scope: **implementation simplification only**, not a new persistence or execution protocol.

Five SessionClient operations (`Load`, `LoadState`, `Timeline`,
`Refresh` and `Resume`) previously repeated the exact
`turnID` derivation followed by `requireIdentity` proof.
They now share a private `authorizedTurnID` boundary. It validates
the Host Turn syntax and derives its stable ID **before** loading and
verifying the persisted Session identity and Actor. If any check fails,
the caller never reaches a Turn read or physical Agent continuation.

`Prompt` deliberately retains its special pre-transcript authorization:
the existing Session must be checked before asking a Host-controlled
`TranscriptSource` for potentially private text. No new identity,
cached authorization, bypass, Runtime CAS, Store method or extra read is
introduced.

The existing cross-tenant Session facade regression is extended to
`LoadState`, `Timeline` and `Resume` in addition to `Load`,
`Refresh` and `Prompt`; a foreign Actor cannot request another Agent
effect. The existing same-actor multi-turn and restart tests remain in place.

Separately, the Agent repeated-Tool guard was profiled before refactoring:
an indexed signature map reduced work for 1024 *unique* Tool results but
increased allocations and latency for common repeated-result workloads
(approximately 0.38–0.41 ms vs 0.25–0.26 ms at 1024 batches on the
same Apple M4; 256 batches also regressed). **The attempted optimization
was reverted**, not merged into production. An independent randomized
behavioral oracle and two contrasting microbenchmark datasets are retained
in the third quality/benchmark slice. Full numerical performance gates
remain report-only until stable-hardware evidence exists.

No Kernel statuses, persisted schema, Tool result contract, execution ID,
feature loop, Host router, or compatibility shim changed. Cross-process
recovery and live-model testing remain independent evidence gates.
