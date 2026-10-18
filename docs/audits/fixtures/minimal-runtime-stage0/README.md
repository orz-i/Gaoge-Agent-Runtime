# Minimal Runtime Stage 0 — Reproducible, Non-Product Fixtures

These source files are retained solely for the [Stage 0 evidence
report](../../2026-10-15-minimal-runtime-stage0-evidence.md).
They are **not** a production SQLite Store, not part of a supported
Go module, and must not be used in applications.

Run commands below from the **repository root** with a Go toolchain
compatible with the current workspace (the evidence used Go 1.27.2).
Network access or a populated module cache is needed to resolve
`modernc.org/sqlite v1.60.1` and its dependencies.
Nothing installs a new dependency in the repository.

## 1. Real Agent state growth (Memory Store)

```bash
cd go/agent-runtime
for steps in 1 10 100 1000; do
  go run ../../docs/audits/fixtures/minimal-runtime-stage0/agent_growth.go \
    "$steps" "/tmp/gaoge-stage0-state-$steps.json"
done
```

The Model and Tool are deterministic test fakes, but `agent.Runner`,
`kernel.Runtime`, `tools.Registry` and their persisted `runState` are
real SDK code. 1,000 steps is a resource-heavy stress case, not a
product default. For sampled CPU/heap allocation evidence, optionally
set `GAOGE_STAGE0_CPU_PROFILE=/tmp/gaoge-stage0-cpu.pprof` or
`GAOGE_STAGE0_HEAP_PROFILE=/tmp/gaoge-stage0-heap.pprof` before
running the fixture, then inspect with `go tool pprof -top` or
`go tool pprof -top -alloc_space` respectively.

## 2. Isolated modernc SQLite fault/concurrency test

```bash
repo="$PWD"
tmp="$(mktemp -d)"
cp "$repo/docs/audits/fixtures/minimal-runtime-stage0/sqlite_fault_test.go" \
   "$tmp/stage0_test.go"
cd "$tmp"
GOWORK=off go mod init gaoge-stage0-sqlite-fault
GOWORK=off go mod edit -go=1.27
GOWORK=off go get modernc.org/sqlite@v1.60.1
GOWORK=off go test -race -count=3 -v -timeout 90s ./...
cd "$repo"
rm -rf "$tmp"
```

Tests cover atomic rollback, stale epoch fencing, failed SQLite WAL
read-to-write upgrade, two subprocesses competing for one ReadyWork,
and process death before/after COMMIT. A normal unit test cannot
prove power-loss durability or supported Android/iOS execution.

## 3. Real Agent snapshot changes mirrored into SQLite

```bash
repo="$PWD"
tmp="$(mktemp -d)"
cp "$repo/docs/audits/fixtures/minimal-runtime-stage0/agent_sqlite_trace.go" \
   "$tmp/main.go"
cd "$tmp"
cat > go.mod <<EOF
module gaoge-stage0-trace
go 1.27
require (
 github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime v0.1.0-beta.22
 modernc.org/sqlite v1.60.1
)
replace github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime => $repo/go/agent-runtime
EOF
GOWORK=off go mod tidy
GOWORK=off go build -trimpath -o ./stage0-trace ./main.go
./stage0-trace 100 /tmp/gaoge-stage0-trace-100.json
# Optional high-write-volume / high-allocation stress:
# ./stage0-trace 1000 /tmp/gaoge-stage0-trace-1000.json
cd "$repo"
rm -rf "$tmp"
```

**Important:** this writes to both Memory Store and an instrumented
SQLite table **after** each Memory commit. It is deliberately not an
atomic `kernel.Store` implementation. Use only its SQL write timing,
changing snapshot sizes and WAL byte counts as I/O evidence. WAL
autocheckpoint is disabled in this fixture to make the total write
volume observable; production must manage checkpointing.

## Reporting

Record commit, Go and SQLite versions, OS/arch, `CGO_ENABLED`,
driver/libc versions, success/failure and any running background jobs.
Compare results only on pinned hardware and toolchain. Both
reference sources and temp SQL programs use fakes or reference schemas:
the real adapter must separately pass the existing
`conformance.RunKernelStoreSuite` and the ADR crash-recovery gates.
