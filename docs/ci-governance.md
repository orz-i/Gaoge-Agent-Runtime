# CI governance: P0

P0 preserves the complete validation scope. It does not introduce affected-only
tests, race sharding, test-result reuse, or a new development branch.

## Safely separating PR description checks

`pr-description.yml` reports **Pull request Markdown description** with a
separate concurrency group and read-only permissions. It needs no Go toolchain,
workspace dependencies, database services, or publication credentials.

This first stage deliberately retains `edited` and the description validator in
`ci.yml`. Removing either before branch protection requires the new check would
allow a malformed description edit to bypass the old required quality gate.
Consequently, **editing a description still reruns heavy CI during this stage**.

An administrator performs the cutover only after this PR is merged:

1. Update existing open PRs to the accepted default branch and wait for the new
   description check to succeed on their current test-merge commits. The script
   does not modify PR branches or bodies for you.
2. Inspect a read-only readiness and branch-protection plan:

   ```bash
   node scripts/activate-pr-description-check.mjs orz-i/Gaoge-Agent-Runtime
   ```

3. After reviewing it, explicitly authorize the additive protection update:

   ```bash
   node scripts/activate-pr-description-check.mjs orz-i/Gaoge-Agent-Runtime --apply
   ```

   The script requires the workflow on the default branch, paginates open PRs,
   rejects missing/failed/skipped/cancelled description checks, retains every
   existing required check and app binding, and preserves strict protection.
   No repository settings are changed without `--apply`.
4. In a follow-up PR, remove `edited` and the legacy description-validation step
   from **only** `ci.yml`, and update the staged-rollout assertion in
   `activate-pr-description-check.test.mjs`. Keep `edited` in
   `pr-description.yml`. At that point, editing a description reruns only the
   independent required check and no longer cancels the code-validation run.

Do not use top-level path filtering to skip required workflows, and do not
rename existing required checks during this cutover. Rollback restores the old
CI trigger/validator before removing the independent required check.
