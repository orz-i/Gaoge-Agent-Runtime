# Long-history allocation: measure before changing persistence

## Correction to the initial structural hypothesis

`consumeLastModelInvocation` already releases consumed `Request` and
`CatalogFingerprints`. The earlier comparison inferred repeated historical
request-prefix retention from the struct shape without following consumption;
that inference was too broad. This change does **not** introduce another stored
state format, delete historical receipts, or claim a measured database bottleneck.

## Verified remaining waste and scoped repair

The public history projection previously deep-copied opaque Provider replay in
Responses and active Request messages, then discarded it. The new path excludes
that replay before cloning, while preserving ordinary message content and full
deep-copy isolation. The original private receipt and model continuation remain
unchanged. A second redundant stored-response clone at consumption is also
removed; the returned consumed invocation is still separately cloned.

Equivalence and mutation-isolation tests compare the new public projection to
the original algorithm. A consumption regression asserts that the request is
released and mutating the returned replay/Usage cannot alter the stored receipt.

## Reproducible microbenchmark

```sh
go test ./go/agent-runtime/agent -run '^Test(PublicHistory|ConsumedInvocation)' \
  -bench '^BenchmarkPublicHistoryProjection$' -benchmem -benchtime=100ms -count=2
```

Local Apple M4 observations, 2026-11-19. Each fixture response has a 64 KiB opaque
replay; **only the active last invocation retains its request**, matching the
existing consumption lifecycle. Both algorithms run in the same process:

| History entries | Original B/op | Filter-before-clone B/op | Original ns/op, observed range | New ns/op, observed range |
| ---: | ---: | ---: | ---: | ---: |
| 8 | 744,781–744,782 | 6,560 | 57,461–60,546 | 1,402–1,408 |
| 64 | 4,927,194–4,927,202 | 56,608 | 251,589–266,715 | 10,442–10,495 |
| 256 | 19,239,644–19,239,646 | 200,993 | 772,339–790,553 | 41,954–43,557 |

This is about 99% less allocation in **this projection construction path for
this replay-heavy fixture**, not a 99% reduction in complete API/Run memory,
storage bytes, WAL, recovery latency, inference cost or production wall time.
`ViewState` still decodes the authoritative state; ordinary content and receipts
still grow with history. Text-only workloads need not show the same gain.

Further storage optimization requires measured snapshot/write/recovery evidence
and must preserve original request hashes, receipts, signed replay, privacy
projection and crash recovery. No lossy compaction or extra archive authority is
introduced here.
