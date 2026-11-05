# Harness top-level Store receipt authority — independent P1 remediation

## Baseline and finding

- Audited HEAD: `2105b0e4dac5d633e60b527fb49ac9b3250d0d3e`, branch `governance/harness-agent-minimal-hardcut-20261104`.
- Earlier unaffected baseline: `main@fb84fc1e56612bf912d8e252b1cd1b8d2625f45a`.
- P1: after extracting the common top-level start envelope, `StartAgentTurn` used the `Turn.ID` returned by `Store.CreateTurn` to derive the root Invocation identity. A custom or inconsistent Store could durably create the requested Turn but return a *different, already persisted* Turn. The Agent would create its Invocation and send the physical Model request under that other Turn, then report a successful completion. In the original baseline, the Agent Invocation retained the Host-derived Turn ID and this scenario did not complete the foreign root.
- The pre-fix independent fault probe measured **one physical Model invocation, a foreign completed Turn and no returned error**. The probe was an intentionally inconsistent Store; there is **no claim** that built-in Memory or PostgreSQL Stores ordinarily produce this mismatch.

## Correction (no compatibility path)

In the one shared `persistStartEnvelope` admission boundary, before any root Invocation, Model/Tool dispatch, Application callback or shared-budget reservation:

1. Check `CreateSession` returned a valid Session with the **same ID, HostThread and Actor** as the Host-selected candidate.
2. Check `PutConfigSnapshot` returned a valid Config with the **same ID, TurnID and ContentHash** as the sealed candidate.
3. Check `CreateTurn` returned a valid Turn with the **same ID, SessionID, HostTurn and ConfigSnapshotID** as the deterministic candidate. A fresh `CreateTurn` must return the original `accepted` state at Revision 1; an idempotent replay can return an already-advanced Turn as long as its identity remains fixed.
4. Any contradiction fails closed with `ErrConflict`. No alternate identity is manufactured, no automatic rebind, no new Store method or schema, no legacy fallback.

The checks use the **existing first-party Store identity predicates** and keep original Store transport errors intact. The fix applies uniformly to Agent, Team, GroupChat, Workflow and Application via their common top-level Turn envelope.

## Negative regression evidence

Permanent tests in `top_level_store_receipt_fence_test.go` were RED against the unfixed `2105b0e` tree:

- 11 injected inconsistent responses covering Session ID/Actor/HostThread, Config ID/Turn/Hash, Turn ID/Session/HostTurn/Config and an invalid fresh Turn status.
- Four independent top-level Feature paths (Agent, Team, Workflow, Application) given a foreign **persisted** Turn return. These must create **no Invocation** and execute no Model or Application effect.
- Idempotent completed-Agent replay with a later mismatched `CreateTurn` return; must return `ErrConflict` without another physical Model call.

All permanent cases are GREEN after the fix. The prior independent `CreateTurn`/foreign-persisted-Turn audit probes were also rerun using *new post-fix assertions* (no Invocation, no Model call, `ErrConflict`), with `go test -race`: GREEN. The original pre-fix probe source remains unchanged in `/private/tmp/gaoge-agent-start-independent.8wkOIL/turn_probe_test.go`.

## P3 documentation correction

The preceding CHANGELOG claimed all five Feature types used the shared first-admission result settlement. In reality they share the top-level admission envelope, but **Application uses its Host-owned effect and OutputRefs settlement**, while Agent/Team/GroupChat/Workflow share the Kernel-backed settlement. Corrected the unreleased beta.24 entry. No Runtime API behavior change.

## Validation and remaining gates

- PASS: new Store receipt mismatch matrix and original/updated audit fault probes.
- PASS: full Harness `go test ./... -count=1` and `go test -race ./... -count=1`.
- PASS: Harness PostgreSQL `go test ./... -count=1`, Harness `go vet ./...` and `golangci-lint run ./...` (zero issues).
- **Not yet evidence:** actual PR-head GitHub CI, real external PostgreSQL/Redis fault integration, live Provider, production/mobile acceptance. These cannot be inferred from local tests.
- Existing trust boundary: a malicious Store can lie about future reads, or about other API results outside this fixed top-level envelope. This change rejects *returned* admission contradictions; it does not claim arbitrary custom Stores are secure.

**Local closeout:** a bounded P1 correction and documentation fix, using the established six-state Kernel lifecycle and existing Store contract. No new compatibility shim, persisted authority, database migration or Feature routing. The untracked user-owned OpenAI probe scripts remain untouched.
