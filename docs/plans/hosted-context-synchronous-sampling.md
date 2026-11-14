# Hosted Context synchronous sampling adaptation

This is a limited extension of the existing concrete BoundedContextAdapter, not
a managed suspension, policy-normalization, Store or schema protocol.

## Original ownership

Host normalization, model binding saves, role/capability projection and catalog
authority retain their existing order. The explicit ContextPreparation selector
accepts one actual concrete sampling adapter in the Run chain, not an arbitrary
Prepare callback. It cannot coexist with ManagedContextRecovery or an external
PrepareModelRequest. The ordinary Prepare API remains opaque.

Workflow hydration performs the original read-only parent, relation, approval
and frozen-input checks, followed by Harness Context hydration. Context owns
Hydrate -> Next -> return. The existing concrete BoundedBudgetAdapter owns its
original registration, accounting, projection and actual return. Other
middleware is not certified by this change.

## Private same-call evidence

The Runner enters a separate synchronous ticket only while the exact selected
Context Run is actually active and its hydration operation has succeeded in the
same scope. Its private token is the ticket identity, nonce and hash of full Run
snapshot plus original request, bound to that adapter and scope. Host ports
receive only cloned values, never the token or disposition.

Only the concrete finite algorithm can return prepared or hard-rejected. A
port error (even BudgetExceeded), uncertain ACK, invalid receipt or panic is
unknown. The invoker validates prepared output before recording that same
ticket, records full source/request/result/disposition, then calls Returned with
the actual error. Only its own verified final hard rejection omits this call's
unknown flag. Existing unknown/invalid tickets are never cleared. A receipt
does not complete an unreturned outer layer.

## Frozen checkpoint cursor

The initial plan captures full Run source, Turn, checkpoint, binding and
canonical computation inputs. The cursor carries separate
ExpectedTurnCheckpointID and ExpectedHeadCheckpointID fields. Only an already
bound sampling branch sets both to its prior checkpoint; this rule is not an
initializer for a new Turn.

Each model-window/Capture/rollover commit uses the frozen Turn revision and both
expected IDs. The expected output Turn changes only checkpoint/ref, revision
plus one and the predetermined UpdatedAt. Full actual Turn and target checkpoint
are compared before process Bind. Bind compares original process checkpoint,
calls the existing replacement operation and reads the whole new checkpoint.
Only then does the cursor advance. No read of latest Turn changes the expected
revision, and no CAS conflict is retried.

A differing session head retains existing detached-branch behavior: the original
Store may commit this Turn's branch without overwriting the foreign head.
Receipt equality does not assert head advancement. A committed checkpoint with
failed Bind remains unknown; it is not rolled back or acknowledged as complete.

## Finite sequence and validation

Model-window CAS precedes live-tail-only compaction. Evaluation rebuilds full
canonical/provider replay input, applies native limits and meters it. Native
compaction returns artifact proposals; Runtime performs exact artifact writes.
Within-budget Capture, read-only artifacts, portable candidates, tool pressure
and protected minimum-tail fallback retain their original sequence. Candidate
evaluation performs no writes; all Put/CAS/Bind receipts are checked exactly.

Required tests cover real finite rejection versus every port's counterfeit
BudgetExceeded/ACK loss, missing or foreign scope, opaque/unreturned layers,
normal canonical requests, original commit-ACK replay, complete artifacts,
full Turn/checkpoint equality, detached head and concurrent Turn conflict.
No result grants TTL replay, general callback completion or managed recovery.

