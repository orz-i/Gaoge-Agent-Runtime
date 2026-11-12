# Issue 107 EA-1 batch 12: Root receipt settlement and recovery

Frozen source: `eaf89e66114b8d02af638a685dc68d579cf909a1`.
Only EA1-AR-S-P3/P4 and EA1-AR-Q-P0/P1/P2/P3/P4 are certified here. Ledger: **82 PASS / 62 UNRUN**. The other 137 rows retain their exact earlier evidence and source SHAs; this is not common-SHA recertification. Whole EA-1/WP-RUNTIME remains NO-GO, WP-GAOGE stays independently blocked, and no remote PR-head gates are claimed.

## Actual boundaries and complete evidence

Start tests invoke actual Root StartRun, validate the frozen request at real Create, and bind the actual full Model request/provider/attempt and complete durable response. Terminal Tool result content/type matches the real Tool result. P3 records the real returned callback while injected Mark failures retain the original scope. P4 actually commits Mark, loses its ACK and faults original Finish; a fresh runner recovers without redispatch. Receipt alone never retires an unknown Host callback. Foreign grant identity, epoch, operation and source fail without full-snapshot mutation.

Recovery tests invoke actual Model/Tool reconciliation after real Root dispatch and lost receipt persistence. P0 rejects the API's expressible stale/foreign sources before verifier execution. P1 tests competing writes/control inside the actual verifier boundary: unbound Apply requires native authority, same-native-token Apply without physical permit is Busy, and Cancel/Acquire/Release cannot mutate original state or revoke the lease. N2 means recovery does not issue a new physical request, not that this phase is untested. Root has no ancestor. These public APIs have no external caller Actor/parent/route parameters, so matching original state does not claim Host authentication.

P2 uses actual Store-clock lease expiry and fresh ownership/reopen; unavailable receipts remain Unknown and cannot replay. The Hostfalse Model contrast persists only authoritative response evidence, never consumption or scope retirement. P3 commits the original receipt CAS then loses its ACK; complete persisted response/RecoveredTool result matches the authoritative verifier return. P4 commits the marker before losing ACK. Fresh public Root recovery completes the original proven retirement without sending again. No test fabricates callback completion or a settlement marker.

Memory retains the same Store and creates a fresh owner, not a persistent restart. Native SQLite closes both handles and reopens the actual database. PostgreSQL uses fresh independent pools. Auxiliary GORM-SQLite shares its test DB across fresh wrappers and is not independent-connection proof. Real PostgreSQL runs both new suites three times without skipped subcases.

## One bounded production correction

The first Memory smoke found an incorrect test expectation: an unbound native Apply had not reached the physical fence. The test now separately proves native admission and same-token/no-physical-permit rejection. Cross-Store execution then demonstrated a real EA-10 error-parity violation for the same unbound Apply: Memory returned ExecutionRequired, SQLite ExecutionConflict, and PostgreSQL Busy. The old logs remain in `issue107-ea1-b12-initial` and `issue107-ea1-b12-error-precedence` with their original source SHAs and checksums.

The correction is limited to ordinary unbound Apply in PostgreSQL and SQLite, under the existing Run lock/writer transaction: an existing native epoch requires native authority before checking physical scope. Control and opaque recovery are explicitly excluded, legitimate leased writes keep their prior path, and same-token/no-physical-permit remains Busy. Existing SQL unit fixtures cover these distinctions without relaxing foreign authority or recovery. This changes the SQL error classification to the already-existing Memory contract; it grants no new write or dispatch capability.

## Same-source verification and review

`evidence/issue107-ea1-b12/` contains twelve completed source-tagged zero-exit logs: minimal Memory fixture smoke, four directed backend race checks including affected SQL unit tests, three linters with zero issues, and full Core/PostgreSQL/SQLite/Harness race suites. The directed checks contain no skips. Commands, ORIGIN and normalized durable-byte SHA256 manifests are included. Final broader repository gates and common-SHA recertification remain outstanding.

Independent limited source review found and closed complete-response/result assertion gaps and the two distinct admission-gate paths at 6b4197e. The same reviewer diagnosed the precise EA-10 error-precedence difference and accepted the limited eaf89e6 SQL delta, explicitly checking Control/recovery isolation. Durable evidence and ledger signoff follows separately; source review alone is not evidence certification.

No push, PR, merge, production migration, or WP-GAOGE edit. Available space was 9.9 GiB before the batch and 9.8 GiB after validation; task cache cleanup was zero. Next fixed batch: AR-R P0 through P4 using actual PrepareRun then Resume, not Start as a substitute.

## Durable independent signoff

The reviewer independently inspected exact evidence commit `7f1fa91b73128b3b86f47b50e0484e3bdb314511` and source `eaf89e66114b8d02af638a685dc68d579cf909a1`. All twelve durable log hashes, source tags and zero exits matched; four directed logs had no skips, both new real-PG suites ran three times, and all four module race runs, three linters and smoke completed. Only the seven specified rows changed UNRUN to PASS; the other 137 rows were byte-identical, total 82 PASS / 62 UNRUN. No Go drift or removal of initial failure evidence was found. Public identity applicability, N2, Hostfalse, backend recovery limits, inventory/strict results and six checker unit tests were accepted within this batch only. Whole acceptance remains NO-GO. The reviewer ran no tests.
