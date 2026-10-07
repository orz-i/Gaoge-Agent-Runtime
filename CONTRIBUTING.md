# Contributing

## Development setup

Install Go 1.26, Node.js 24, pnpm 11.22, Docker, and GNU Make. Then install the
workspace dependencies:

```bash
pnpm install --frozen-lockfile
```

Use the smallest useful gate while iterating, then run the release-quality
checks before submitting a change:

```bash
make unit       # Go + TypeScript unit tests
make check      # format/tidy/vet/test/race/lint/build/consumer contracts
make coverage   # repository coverage floors
make integration # real PostgreSQL and Redis recovery/concurrency suites
make security   # govulncheck + high/critical pnpm advisory gate
```

`make security` requires network access to the Go vulnerability database and
npm advisory registry. A network failure is intentionally not treated as a
successful security scan.

## Architecture rules

Read [docs/architecture.md](docs/architecture.md) before changing package
boundaries. In particular:

- preserve deterministic replay, idempotency, revision CAS, and durable wakeup
  boundaries;
- keep Kernel independent of host, protocol, provider, persistence, and
  telemetry SDKs;
- keep feature semantics in the owning feature package;
- make background workers, migrations, credentials, and policies explicit host
  composition;
- do not make observability a correctness dependency.

## Public contract changes

Treat these as compatibility-sensitive changes:

- exported Go constructors, required interfaces, and public types;
- HTTP routes, status/error behavior, OpenAPI schemas, and JSON fixtures;
- TypeScript public declarations and runtime behavior;
- persisted records, migration semantics, queue/feed recovery behavior;
- release module membership or version alignment.

Update tests and the changelog for intentional contract changes. If callers or
persisted data require migration, add a concrete upgrade note under
`docs/releases/`. Do not regenerate fixtures merely to make a failing contract
test pass.

## Evidence by change type

| Change | Minimum evidence |
| --- | --- |
| Kernel/feature state machine | focused unit tests; race tests for concurrent paths |
| Store/queue/feed adapter | conformance tests plus real-engine integration where applicable |
| HTTP/TypeScript wire contract | OpenAPI + fixture + packed consumer checks |
| New Go module/public API | `contracts/consumers/go` fixture and release-boundary update |
| Telemetry | content-safety/cardinality tests and provider-lifecycle boundary docs |
| Dependency/tooling | lockfile update, quality gates, vulnerability audit |

## Commits and releases

Use Conventional Commits and keep commits focused so behavior, tests, and
release metadata can be reviewed independently. Beta releases are coordinated
version cohorts: all released Go modules and the TypeScript archive move
together. See [docs/releasing.md](docs/releasing.md) for the tag and release
workflow.
