# Releasing a Beta

The repository uses one root release tag plus one Go module tag per module. All
tags for a release must point to the same accepted commit.

## Preconditions

1. Update `VERSION`, package metadata, internal Go requirements, and the
   changelog in one pull request.
2. Run `pnpm install --frozen-lockfile`, `make beta`, and verify a clean
   worktree.
3. Merge the accepted commit to `main` and protect it from force pushes.
4. Configure the `NPM_TOKEN` repository secret for the `@orz-i` public scope.

## Create and push tags

```bash
node scripts/release-tags.mjs --version 0.1.0-beta.1
node scripts/release-tags.mjs --version 0.1.0-beta.1 --create
git push --atomic origin \
  v0.1.0-beta.1 \
  go/agent-runtime/v0.1.0-beta.1 \
  go/agent-runtime-harness/v0.1.0-beta.1 \
  go/agent-runtime-harness-postgres/v0.1.0-beta.1 \
  go/agent-runtime-mcp/v0.1.0-beta.1 \
  go/agent-runtime-a2a/v0.1.0-beta.1 \
  go/agent-runtime-postgres/v0.1.0-beta.1 \
  go/agent-runtime-redis/v0.1.0-beta.1 \
  go/agent-runtime-http/v0.1.0-beta.1
```

The root tag starts the release workflow. It re-runs the Beta gate, verifies
that every module tag points to the same commit, publishes the TypeScript
package with npm provenance and the `beta` dist-tag, and creates a GitHub
prerelease.

Never move a published tag. If a release fails after tags are public, fix the
problem in a new prerelease version.
