# Beta support policy

## Supported surface

The packages marked **Supported** in the root README receive correctness,
security, and migration fixes for the current Beta line. Public behavior is
defined by exported Go APIs, the TypeScript package exports, and
`contracts/agent-runtime/v1`.

The documented A2A HTTP+JSON surface is Beta-supported. Other transports remain
outside the supported surface; prerelease APIs can change before `v1.0.0`.

## Compatibility and upgrades

- Only the latest Beta prerelease is supported.
- HTTP and persistence changes include an upgrade note in `CHANGELOG.md`.
- Database migrations are forward-only during Beta. Back up production data
  before upgrading.
- Go callers should pin an exact prerelease tag. TypeScript callers should pin
  the matching GitHub Release archive. npm registry publication is reserved for
  stable releases.

## Getting help

Use GitHub Discussions for design and usage questions. Use GitHub Issues for a
minimal reproducible bug report. Security reports must follow `SECURITY.md` and
must not be filed publicly.
