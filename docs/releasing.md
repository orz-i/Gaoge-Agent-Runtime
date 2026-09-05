# Releasing a Beta

The repository uses one root release tag plus one Go module tag per module. All
tags for a release must point to the same accepted commit.

## Preconditions

1. Update `VERSION`, package metadata, internal Go requirements, and the
   changelog in one pull request.
2. Run `pnpm install --frozen-lockfile`, `make beta`, and verify a clean
   worktree.
3. Merge the accepted commit to `main` and protect it from force pushes.
4. Keep npm registry publication disabled for all prereleases. Beta artifacts
   are Go module tags and the TypeScript archive attached to GitHub Releases;
   no npm publication credentials are required.

## Create and push tags

```bash
node scripts/release-tags.mjs --version 0.1.0-beta.7
node scripts/release-tags.mjs --version 0.1.0-beta.7 --create
git push --atomic origin \
  v0.1.0-beta.7 \
  go/agent-runtime/v0.1.0-beta.7 \
  go/agent-runtime-harness/v0.1.0-beta.7 \
  go/agent-runtime-harness-postgres/v0.1.0-beta.7 \
  go/agent-runtime-mcp/v0.1.0-beta.7 \
  go/agent-runtime-a2a/v0.1.0-beta.7 \
  go/agent-runtime-postgres/v0.1.0-beta.7 \
  go/agent-runtime-redis/v0.1.0-beta.7 \
  go/agent-runtime-http/v0.1.0-beta.7
```

GitHub does not create a push event when one push updates more than three tags.
Because this repository deliberately pushes the complete tag set atomically,
start the release workflow against the existing immutable root tag after the
push:

```bash
gh workflow run release.yml --ref main -f release_tag=v0.1.0-beta.7
```

The workflow checks out the root tag, re-runs the Beta gate, verifies that every
module tag points to the same commit, packs and checks the TypeScript package,
and attaches it to a GitHub prerelease with the reviewed notes in
`docs/releases/<root-tag>.md`. It never publishes to the npm registry. npm
publication remains reserved for a future stable-release workflow. A root-tag
push event remains supported for release sets small enough to emit one.

Never move a published tag. If a release fails after tags are public, fix the
problem in a new prerelease version.
