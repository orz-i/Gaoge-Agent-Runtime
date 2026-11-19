# PR #131 second-audit governance

## Reviewed boundary

The independent re-audit of `8095e4c1` found four P1 protocol gaps despite green
CI. This follow-up repairs the shared authority contracts rather than treating
the six new audit scenarios as unrelated failures. Original physical receipts,
budget accounting and historical opaque custody are not discarded or upgraded.

The subsequent audit of `4b1090f3` identified additional cancellation phase and
independent child-projection boundaries. See [third-audit governance](2026-11-19-pr131-third-audit-governance.md)
for the current fixes and full durable-phase regression matrix; schema v8 is unchanged.

## Segmented changes

1. **Cancellation Attempt admission** (`4d6d1c2e`). The original Runner freezes
   its exact immutable Root/target binding before electing a cancellation.
   Memory and PostgreSQL consume that binding under the same Turn lock as Retry
   and intent insertion. An obsolete child is rejected before intent/timeline/
   budget writes, and a stale Root Cancel cannot fence a replacement after it
   loses the Attempt CAS race. Current-attempt status progress remains valid.
   Raw Store protective intents retain their explicit trusted-Store contract;
   arbitrary errors never release unknown-effect cancellation intents.
2. **Typed capacity-wait history** (`579af432`). Waiting-only observations are
   not physical child admission. A shared predicate verifies their complete
   frozen envelope and exact predecessor chain for both normal terminal CAS
   and no-Run cancellation. Waiting-to-Started-to-terminal history retains its
   original observation while requiring the child and its physical receipts.
   Missing/forged Started effects are never downgraded to harmless Waiting.
   SQL loads the same observation history under its Turn transaction.
3. **Retirement frontier closure** (`2c503c95`). An exact genuine RETIRED callback
   can be acknowledged from its already-persisted completion, owner and source
   under the original Run transaction before a successor advances. One reducer
   serves Memory, PostgreSQL and native SQLite. It covers owner acquisition/
   release, leased writes, native controls, child admission, quarantine and
   managed successor handoff. Unknown completion, active grants, source/owner
   mismatch and ambiguous historical frontiers still block. The original ACK
   identity remains replayable without authority over any later owner/state.
4. **Native SQLite parity and schema v8** (`d0a77aaa`). SQLite implements both prepared and
   owned Root settlement interfaces with the same predicate and transaction
   boundary as Memory/PostgreSQL. Ordinary and managed physical admission
   persist the last physical epoch separately from lease churn. The complete
   Harness lifecycle matrix now runs against all three actual Kernel Store
   implementations, not just GORM SQLite as a proxy for native SQLite.

## Migration and compatibility

- Native SQLite v7 files require explicit `MigrateV7ToV8`, with all old writers
  stopped and a retained backup. Earlier files complete each preceding explicit
  migration. `OpenExisting` never silently changes schema.
- The v8 watermark is derived only from original grant/retirement records,
  not a possibly newer execution lease epoch. Original callback/ACK payload
  bytes and custody classifications remain unchanged; empty legacy custody
  remains unknown.
- Custom Harness Stores must implement the atomic cancellation admission
  contract using the original private Runner binding. A read-before-append
  owner check alone does not arbitrate Retry races.
- The new successor reducer is not a generic ACK capability. It consumes an
  authoritative Store's persisted original facts under its own lock and does
  not infer effects from an error, missing grant, TTL or terminal-looking Run.

## Regression matrix

| Contract | Positive cases | Required negative cases |
| --- | --- | --- |
| Cancellation admission | Current Attempt, concurrent Retry wins, continued current cancellation | Stale Root control and sequential historical Subtask/Invocation controls cannot append a fence over the successor |
| Waiting history | Waiting-only, Waiting then admitted/completed, no-Run replacement after lost Retry ACK | Live prior child, missing terminal receipt/transition, substituted waiting owner/envelope and malformed authority JSON |
| Retirement frontier | Before-ACK process death, new Resume/Cancel, Acquire/Release, ordinary mutation, descendant admission, quarantine, managed handoff | Active grant, unknown/legacy completion, foreign owner/source, duplicate frontier; old ACK confirmation cannot revoke successor |
| SQLite parity | Prepare/Cancel, safe/opaque callback history, repeated generations, historical completion, released undispatched lease | Same physical/ACK custody restrictions as Memory/PostgreSQL, v7 implicit-open rejection, original migration bytes unchanged |

`TestRealPostgresRootGenerationLifecycle` invokes the expanded matrix through
the real-service runner's exact `TestRealPostgres*` selector. With
`TEST_POSTGRES_DSN`, SQL cases use isolated PostgreSQL schemas and durable SQL
RunRelations. Native SQLite cases always use actual private on-disk databases.

## Verification

- Existing affected Core/Harness/PostgreSQL/native-SQLite recursive race suites
  pass, including the shared owned-retirement conformance suite.
- The expanded real-service lifecycle matrix passes three race-enabled
  iterations against isolated PostgreSQL 16.16 alongside Memory/native SQLite.
- Workspace formatting, vet, golangci-lint and isolated-module tidy checks pass;
  the native SQLite test dependency and its transitive module sums are explicit.
- Full `make check-no-race` passes, including isolated-module tidy, release
  consumers and examples. The dependent Provider/HTTP/A2A/Redis/MCP/OTel/
  transport recursive race suites also pass.
- `make integration-test` passes against private PostgreSQL 16.16 and Redis
  8.10.3 instances, including the expanded lifecycle matrix and continuation
  E2E suite. Both temporary service instances were shut down after validation.
- All six re-audit scenarios (with a production-complete Waiting envelope)
  pass with `-race -count=3` against real PostgreSQL, Memory and native SQLite.
- New-commit CI is still the authoritative remote gate, not an inherited green
  result from the reviewed baseline. No product repository or SDK pin is changed.
