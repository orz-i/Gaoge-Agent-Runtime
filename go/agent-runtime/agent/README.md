
### Bounded first-party callback orchestration

`BoundedContextAdapter` and `BoundedBudgetAdapter` are explicit alternatives to
ordinary Host middleware. Runtime owns their complete operation order; Host ports
retain authorization, storage, limits, metering policy and projections. Context
preparation persists artifacts and commits the original checkpoint CAS before
binding the returned checkpoint. Budget owns the original reservation, dispatch,
proven-unsent CAS and settlement sequence. A failed Host write/projection is not
converted to a successful callback receipt.

Only these concrete adapters (and the channel-only `ModelAdmissionBarrier`) can
record internal, invocation-bound lifecycle evidence. A wrapper with the same
name, a public state hash, a nil Provider count, or a middleware-supplied boolean
cannot create that evidence. Every entered callback and Next must return. The
receipt binds the original Run, physical grant, native owner/epoch, operation,
layer nonce and request/source; it never upgrades a previously persisted unknown
Host completion or authorizes an expired owner to send again. Ordinary middleware
continues to preserve uncertainty on an error.
