# Harness protective cancellation: immutable Invocation authority vs. presentation data

## Fix scope

- Independent review baseline: `e55c473ffe7e48ddf2b15c7949b577776f98ddee`.
- Code remediation commit: `01d70cad3a03c07b139c23f7831f1c4f2289b188`.
- One conditional P1 with two paths: `Runner.Cancel` and `Runner.CancelInvocation` rejected an already running, correctly owned physical Run when a custom Store returned a malformed `Invocation.OutputRefs` display field. The previous baseline `10b97c9` had cancelled the same Run, even when final presentation returned an error. The regression was in the extra `validInvocation` check added during I1 remediation, not a demonstrated fault in an ordinary built-in Store.
- Full independent audit and standalone reproduction material: `/private/tmp/gaoge-after-protective-independent-20261104/report.md`. Source identity remains Host/Store-authoritative; no new persisted field, public API, migration or legacy bridge.

## Single operation-aware trust boundary

1. **New execution, replay, recovery, and Host-visible projection** continue to require complete `validInvocation`, including the frozen identity, lifecycle, actual InputHash, and valid `OutputRefs`.
2. **Protective stop of an already admitted execution** requires `validInvocationExecutionAuthority`: unchanged immutable Invocation ID, parent/owner Turn, CapabilityKey/DefinitionVersion, ExecutionClass, frozen Input + InputHash, ExecutionRefID, Attempt and valid lifecycle. This shared validation deliberately excludes Host-visible OutputRefs; no display metadata is an execution-owner certificate.
3. `verifyProtectiveInvocation` compares those immutable fields against **both** indexed Store reads, `GetInvocation(ID)` and `GetInvocationByExecutionRefID(RunID)`, before creating a cancellation intent or attempting a physical stop. This is critical: simply omitting OutputRefs verification permitted an anomalous custom Store to rotate a Child Attempt while still physically cancelling the old Run; another mutation could return success for a foreign Run ID without stopping the real target.
4. Root Cancel additionally requires the original Turn ownership, root/no-parent identity, and the existing `validateRootRuntimeOwnership` proof against the actual Kernel Run (Actor/Thread/Goal/RunID/RequestID); Child Cancel retains frozen ancestry and physical RunRelation isolation. All existing cancellation-intent, unknown-effect, No-effect, CAS, and descendant-cleanup fences still govern actual effects.
5. A true cancellation can succeed physically while final Snapshot/OutputRefs projection fails. This must return the actual projection error rather than falsely declaring a no-effect stop or retrying an already-sent physical operation. An indexed Invocation identity read failure still prevents cancellation, because it is identity-critical rather than cosmetic; Store availability guarantees remain an explicit dependency.

## RED → GREEN regression evidence

- Original independent `go test -overlay` Root and Child malformed-OutputRefs probes: **RED** before fix, **GREEN** after fix. The actual `kernel.Run.Status` reaches `cancelled`; the Child's parent Run remains `running`.
- Permanent `TestProtectiveRootStopUsesFrozenIdentityNotOutputMetadata` / `TestProtectiveChildStopUsesFrozenIdentityNotOutputMetadata`: malformed/duplicate OutputRefs and unrelated error display information do not block protective stop.
- Permanent `TestProtectiveRootStopRejectsBrokenFrozenExecutionIdentity` / `TestProtectiveChildStopRejectsBrokenFrozenExecutionIdentity`: nine variants per scope, including InputHash, Input payload, TurnID, Attempt, ExecutionRefID, ID, parent, execution class and lifecycle. The physically persisted original Run remains running and the denied stop reports an error. The Child property variants exposed a second edge in the initial patch: changing Attempt could cancel the original Run; this was corrected in the same bounded remediation by requiring indexed immutable identity agreement.
- Existing I1 read-side cross-tenant and Host-derived ID regressions, I2 frozen Config, I3 Invocation, native/remote routed cancellation, replay and Provider receipt tests remain required. They must be re-run against the **final locked SHA** before any merge admission.

## Exit criteria and residual unknowns

- This correction only addresses the verified P1 classes; it does **not** turn a misbehaving Store into an adversarial database sandbox. The Store remains trusted for its atomic CAS, its indexed lookup consistency and its durable physical execution receipts. A malicious adapter returning consistently false answers from every read is outside these invariants.
- A false mismatch on the identity-critical indexed reads fails closed; cosmetic metadata faults may cause a projection error **after** the physical cancellation. That distinction is intentional and must not be collapsed again.
- Do not claim final independent approval, PR CI, live Provider/gateway coverage, or external vulnerability-scan success until specifically performed and recorded against the final HEAD.
