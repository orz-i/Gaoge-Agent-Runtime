# Full same-source AR-Q fixture diagnostic

Source 00d0e69a0d0af64f37df28c98b17cb6b45c63af1, complete 2026-11-13T15:00:44Z. All six compile checks, Root/two-generation/P4/wrapper smoke, formatting, metadata, untruncated twelve-module lint, SQLite/GORM/real PostgreSQL directed count=3, both original continuation race tests count=20, and all-module vet passed. Full twelve-module race completed with nine modules passing and three failing solely the shared AR-Q P4 model/tool fixture.

That fixture loses the first genuine Mark acknowledgement but leaves Finish available. The repaired production path correctly uses the durable marker, completes Finish and atomic ACK, and returns success; the old expectation of an injected error is no longer a valid model of an unfinished retirement. Independent review confirmed no production change is needed. Preserve this original single-ACK-loss case as an AR-Q success control. Separately add an explicitly named second fault: exact-grant Finish blocked before commit, proving the original scope remains and no retirement is written before reopening and actual public recovery. Do not describe the two-fault case as the original single fault, remove receipt/source/no-replay assertions, or change unknown authority.

This diagnostic is not a full pass. The subsequent test-only change must be verified on its own exact source.
