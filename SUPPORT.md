# Beta support policy

## Supported surface

The packages marked **Supported** in the root README receive correctness,
security, and migration fixes for the current Beta line. Public behavior is
defined by exported Go APIs, the TypeScript package exports, and
`contracts/agent-runtime/v1`.

The A2A adapter is experimental. It is tested, but its API may change without a
deprecation window before `v1.0.0`.

## Compatibility and upgrades

- Only the latest Beta prerelease is supported.
- HTTP and persistence changes include an upgrade note in `CHANGELOG.md`.
- Database migrations are forward-only during Beta. Back up production data
  before upgrading.
- Go callers should pin an exact prerelease tag. TypeScript callers should use
  the npm `beta` dist-tag rather than a range that accepts later prereleases.

## Getting help

Use GitHub Discussions for design and usage questions. Use GitHub Issues for a
minimal reproducible bug report. Security reports must follow `SECURITY.md` and
must not be filed publicly.
