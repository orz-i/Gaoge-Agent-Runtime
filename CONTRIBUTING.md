# Contributing

## Development setup

Install Go 1.26, Node.js 24, pnpm 11.22, Docker, and GNU Make. Then run:

```bash
pnpm install --frozen-lockfile
make check
make integration
```

## Change requirements

- Add or update tests for behavior changes.
- Preserve deterministic replay, idempotency, and optimistic-concurrency
  boundaries.
- Document public API, HTTP contract, or persistence changes in the changelog.
- Keep the core module independent of Gaoge host packages.
- Use Conventional Commits and keep each commit focused.

Pull requests must pass unit, race, lint, clean-consumer, PostgreSQL, and Redis
gates. Generated or formatted output should be committed with the source change
that requires it.
