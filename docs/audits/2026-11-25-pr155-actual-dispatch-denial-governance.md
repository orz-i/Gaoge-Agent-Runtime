# PR 155 — actual native Tool dispatch refusal

Base: `688d6f28`. Local governance following independent audit; not merge admission.

## Contract

- The last Ledger admission read is not physical authority. The actual
  native `DispatchTool` CAS checks the exact immutable Tool reservation,
  its registered Run and elected Ledger cancellation. A definitive
  cancellation returns the observed original zero-use, undispatched ledger;
  a failed write may return a speculative dispatched candidate and does not
  qualify. No additional pre-dispatch recheck or cross-store atomicity claim.
- Run and Tool use the same unique, private `nativeAdmissionDenied` return
  identity, issued only by the actual trusted adapter after original
  acknowledgement/projection, before Next. ToolCallAuthority accepts only
  the original empty result and exact returned denial. CallbackScope requires
  every actually entered layer of that exact Tool attempt to have returned
  its own trusted receipt; the enclosing original Grant's complete lifetime
  remains separately necessary. Opaque entered layers (even a pass-through),
  changed/masked errors, forged results and failed projections cannot qualify.
- The qualified Tool denial consumes the existing unsent disposition and
  revision-fenced closure. It can seal the existing `OwnedToolReturn.Unsent`
  proof across closure CAS failure. Native parented terminal failure binds
  the exact composed native return, rather than treating its two original
  causes as an unrelated Host error.
- Existing elected Harness cancellation/deferred cleanup also consumes
  sealed original no-send evidence before draining any already-dispatched
  child receipts. No new business Resume or result lookup for the denied Call
  is required. Original terminal failures, when already committed, remain
  Failed rather than being rewritten to Cancelled; if no terminal CAS
  committed, the elected Kernel control can close as Cancelled.
- No new durable state machine, SQL schema, Grant type, custody classification,
  receipt owner or cancellation owner.

## Local verification

- Original real Harness cancellation race passes with no Timeline, Timeline
  outer and Timeline inner. Cancellation commits after the last read and
  successful native owner check; actual Dispatch refuses; the original
  Handler never runs and no Tool charge or result exists. A second elected
  Harness.Cancel completes the original fence. Outer Timeline closes its
  actual entered attempt with Started/Failed, not a provider receipt.
- Harness closure CAS cut (two failures) preserves sealed original no-send
  proof; the same elected cancellation completes via existing receipt-only
  cleanup, without configuring a ToolOutcomeVerifier.
- Sixteen new shared actual-dispatch cases include metered original denial,
  both Timeline orderings, parented, closure CAS/reopen, projection and
  Timeline failures, opaque before/after/pass-through/mask/forged result,
  unentered opaque inner layer, Dispatch write before/after commit (typed
  error collision), and actual Handler error. Both mixed Child batch cases
  (closure success and two CAS cuts) settle only the two original physical
  siblings, then finish cancellation. Ledger unit tests cover both sides of
  the actual cancellation/Dispatch CAS and original request identity;
  CallbackScope tests reject cross-attempt and untrusted layer evidence.
- Shared original receipt/approval/capacity/unsent/actual-denial matrix:
  Memory, native SQLite, GORM, independently reopened encrypted SQLite
  Kernel/Harness ledger, actual PostgreSQL Kernel/Harness ledger (79 leaf
  cases). Core/Harness packages, directed Race, workspace fmt/tidy and
  targeted Core/full Harness lint pass.

## Boundaries

The qualified actual denial is specifically the original elected Ledger
cancellation, not every Ledger conflict. Unexpected foreign/released
non-cancelled reservation or uncertain Dispatch acknowledgement remains
fenced, as does any actual/unknown downstream effect. The outer first-party
Timeline is observational and may already have recorded its entered failed
attempt; no synthetic physical result is minted. Harness interleavings run
against Memory stores; real PostgreSQL covers the shared Agent/ledger
matrix, not a cross-process Harness scheduling/cancellation workload.
The previous conservative process-death and parented irrecoverable ACK
boundaries are unchanged. No commit, push, merge or deployment performed
in this governance batch.
