# Explicit WorkingState

WorkingState is optional, bounded task data supplied by the Host through
`harness.ContextSeed.WorkingState`. It records a goal, constraints, completed
work, pending work, decisions and evidence references. It does not generate a
summary or replace the Host transcript. The existing Context Manager, Harness
admission and checkpoint Store remain authoritative for their existing roles.

State updates use the existing explicit rollover boundary. Session composition
remains separate from execution. This stage adds no Agent loop, summarizer,
memory retrieval or background process.

## Supplying state

A state update must refer to a real previous checkpoint on the selected source
path. Open the first Turn without a state. On a later Turn, build the complete
Host source path, resolve its reusable boundary, then supply the state and the
current user message together:

```go
// session is the existing OpenAgentSession/OpenSession handle. This lookup
// can run in its TranscriptSource after the Host has loaded source ancestry.
boundary, err := session.ResolveContextSourceBoundaryForPath(ctx, previousTurnID, instructions, sourcePath)
if err != nil { return nil, err }
if boundary.CheckpointID == "" { return nil, harness.ErrConflict }

seed := &harness.ContextSeed{
    SourcePath: sourcePath,
    Instructions: instructions,
    Entries: entries, // the actual current user message must remain last
    WorkingState: &runtimecontext.WorkingState{
        Version: nextHostVersion,
        Source: runtimecontext.WorkingStateSource{
            CheckpointID: boundary.CheckpointID,
            ContentHash: boundary.ContentHash,
            CoveredThroughSourceID: boundary.CoveredThroughSourceID,
            CoveredPathHash: boundary.CoveredPathHash,
        },
        Goal: "Prepare the release report",
        Constraints: []string{"Use the recorded test results"},
        Completed: []string{"Compared the two builds"},
        Pending: []string{"Explain the remaining platform failure"},
        Decisions: []string{"Keep the durability checks enabled"},
        EvidenceRefs: []string{"host-message:test-report-42"},
    },
}
return seed, nil
```

The Host owns state versions and their meaning. Versions are positive integers
bounded by `runtimecontext.MaxWorkingStateVersion`;
an explicit replacement must exceed the selected base state's version. The
four source fields must match the selected checkpoint exactly. A full source
path uses its nearest reusable source-aligned checkpoint, which can differ from
the Turn's latest checkpoint after runtime messages have been captured. Use
`ResolveContextSourceBoundaryForPath` for that path. A source delta instead
names its original `BaseCheckpointID` explicitly and supplies only the exact
uncovered transcript suffix.

The boundary lookup authenticates the Session handle against the frozen owner
of an existing `previousTurnID` and reads existing checkpoints. That Turn may be
any authorized Turn in the same Session; it is an ownership proof, not the
source-path selector. The lookup does not dispatch a model or Tool. If another
Turn advances the selected boundary before admission, refresh the source binding for a new Turn;
do not reinterpret an already admitted Turn's frozen input.

Resolve and freeze a new update once per Host Turn. Retries must return the
original seed, including its original source binding and version; resolving a
new boundary inside every retry would change the admitted request and correctly
produce `ErrConflict`. The Host can persist that submitted seed with its own
Turn data, as it already does for stable transcript identities.
Once admission has succeeded, retrying an interrupted checkpoint write uses
that originally validated base even if another Turn has since advanced the
active head.

Harness also freezes the base when no explicit update is supplied. The derived
`ConfigSnapshot.ContextBase` records the original Open plan, including an
explicitly absent previous checkpoint, and is sealed before the Turn is
created. It is Runtime metadata; Host-supplied values are discarded. A retry
checks the original Host input and configuration, then restores that sealed
plan. It neither acquires a later state nor increments the carried version.
If another Turn moved the active head, the existing checkpoint commit keeps
the recovered Turn as a detached branch instead of rolling the head back.

## Lifecycle and trust

The optional field is included in the admission hash and the checkpoint's
content hash. Each explicit update creates a new Context generation with the
actual selected checkpoint as its parent. Ordinary Turns that omit an update
carry the selected branch's state once. Replaying an admitted Turn cannot
replace its state; recovery reads the committed checkpoint and any already
frozen ModelInvocation request.

State is separate from `Window.Entries`: it does not claim an original Host
`SourceID` and is not repeatedly appended to history. Materialization emits a
single user-role JSON block after static instructions. That block labels the
content as derived task data, not instructions or Tool authorization. The real
current request remains in the transcript. Changing WorkingState cannot select
a Tool, grant approval, enlarge an allowance or change the model output limit.

A state can be carried only along compatible source ancestry and static
configuration. A branch reset cannot inherit the active sibling branch's state;
returning to an established branch can reuse that branch's own checkpoint.
Malformed, mismatched or stale explicit updates fail before model dispatch.
Checkpoint cloning owns copies of every state list.

The state JSON is limited to 16 KiB, each text field to 2 KiB, and each list to
32 items. Canonical UTF-8 is required. These bounds are checked before admission;
the existing final request accounting still enforces the actual model input
window and reserved output. A state that leaves insufficient room fails the
same budget gate as any other protected Context prefix.

Compaction preserves the state as a protected prefix and archives only the
original transcript messages and Tool results. Existing exact Artifacts and
their scoped read Tool remain available. Evidence references are Host-supplied
references: the checkpoint binding proves which source projection the Host
used, not the truth of the assertions. The Host retains original messages;
WorkingState does not create a second evidence Store.

## Compatibility and acceptance

Absent state and absent base metadata are omitted from JSON. Existing
checkpoint, admission and sealed Config hashes retain their prior semantics.
New Context admissions add the optional derived base metadata to their sealed
Config; Store interfaces and HTTP snapshot DTOs remain unchanged. No database
schema migration is required. Older executables must be upgraded before
handling the new Config metadata or checkpoints containing WorkingState.

Legacy Turns that already have a checkpoint restore it normally. For an old
accepted Turn without a checkpoint or base binding, an explicit source delta
still identifies its immutable base. A legacy full-path retry that would now
inherit WorkingState from a newly selected base fails closed with a conflict:
the original admission did not freeze that state's provenance. The Host can
inspect the original facts through the existing trusted read APIs; the runtime
does not rewrite the old hash or invent the missing provenance.

Acceptance reuses the existing Context, Session, rollover and durable recovery
fixtures. It checks exact source binding, version and size rejection, one state
block across Turns, branch isolation, preserved raw Artifacts, pending-intent
recovery and unchanged Tool authority/output ceilings. Deterministic fixtures
establish these invariants; they do not claim a task-quality improvement or an
automatic reduction in model input. [Automatic semantic summaries](automatic-working-state.md)
are a separate, explicitly configured Stage 4 feature; explicit Host updates
keep their original priority and do not invoke that path.
