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

## Follow-up: real SDK long-Run growth, not the public-view microbenchmark

The executable `go/agent-runtime/examples/long-run` now measures aggregate state
submission at the real fenced Store port and checks a committed-receipt ACK-loss
recovery. Single local observations on Go 1.27.4, darwin/arm64, with 1,024 text
bytes per read and zero synthetic replay:

| Read steps | State writes | Final state bytes | Maximum state bytes | Cumulative submitted state bytes | Elapsed ms | First model after Resume, microseconds |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 706 | 176,525 | 301,806 | 87,817,179 | 667 | 4,704 |
| 500 | 3,506 | 882,350 | 1,509,230 | 2,174,555,457 | 15,153 | 24,944 |

Both completed exactly the requested Tool executions, retained zero consumed
request transcripts and performed no committed call twice after reconstruction.
This identifies the next investigation target: with five times the steps,
final state grew about fivefold but cumulative full-state submission grew about
25-fold in this unbounded-history fixture. That is **not** evidence that all
historical request prefixes are retained, nor a 25-fold database WAL result.

Process heap samples were 6,740,112 and 29,087,376 bytes respectively; sampling
can miss the true peak. The fixture does not measure disk/WAL and does not
survive Memory Store process exit. These single timing observations are not
stable-runner statistics, a production SLO or a measured storage improvement.
No new storage format is justified solely by the earlier public-view result.
