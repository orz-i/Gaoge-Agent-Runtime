# 2026-11-08 — Bounded queryless Context navigation and evidence-strategy metrics

## Baseline and actual observation

The saved official OpenAI `gpt-5.6-terra` `distractor-05` pilot report
`20261108T022700Z-85cf9d6e.json` has SHA-256
`12a851e73ff4abe82a24fef91638e2629c7c4df3ef0eabce8fa73cdc8f21c9e5`.
It was verified without modifying the report. Its source is
`33c876b`, so its `task_did_not_reach_interruption` failure classification
predates PR #97's more discriminating attribution. The observed task made
7 model calls, 7 tool executions, 6 successful Artifact reads and **no draft**;
the final captured `response.completed` included an
`read_context_artifact` call with only `offset=98648`, `limit=8000`,
and no `query`. It is a **fact** that this last call did not activate
candidate search. This is **not proof** that all earlier reads were
offset-only, that the Provider rejected anything, or why the original Run
terminated.

The 128,000 `maxOutputTokens` configuration is not output consumption:
the task had 979 total observed output tokens across 7 model calls, and
the Host's input cap remained 32,768 for both 128,000 and 4,096 output
reservations on its 1,050,000 model window. This change is unrelated to
loosening or tuning output ceilings.

## Scope and trust invariants

1. **Host Tool capability, not Agent routing.** Only the *opted-in*
   `ContextArtifactCandidateToolRegistration` changes. Large artifacts
   addressed by offset with no query now return a **small exact original
   prefix + forward-only original-rune cursor + content-free `searchHint`**
   within 512 total serialized bytes. The ordinary literal reader's behavior
   and Tool policy remain byte-for-byte unchanged. Small enhanced reads and
   explicitly searched reads keep their original semantics.
2. **No answer injection.** The constant hint says to use the `query`
   argument rather than scanning a large artifact. It never contains the
   release target, source facts, preferred candidate, hidden Tool grant,
   identity, Artifact ID, model prompt or caller data. The original exact
   Artifact is unchanged. A hint is *not evidence*. Actual facts still require
   a new authorized exact read. No Tool output is silently converted into
   a completed draft or final answer.
3. **Bounded visibility.** A long `limit=8000` offset-only read originally
   produced a result that was immediately re-archived under the 512-byte
   Context Tool-result policy. Its original appended search hint would not
   have been visible to the next Model call. The new enhanced policy first
   limits the raw original prefix, then, if needed, clips only the suffix
   until the complete JSON+hint is <=512 bytes. `nextOffset` is moved by
   precisely the retained Unicode-rune count and never points outside the
   source. `done` is recalculated. No round-trip rerun or extra physical
   effects are introduced.
4. **Measurement identity.** The enhanced Tool policy version is now
   `harness-candidate-search-v2`. The OpenAI quality dataset hash changes
   from `sha256:d22c61a16559e72d0c75c8f61ba342ac16c9382d3a55afc6a5622d8d4f70624b`
   to `sha256:7905b6abca186865869bcdc89ad8178cc2cb2ec4b5b1ada64aea4356ec97f992`.
   The scripted calibration hash changes to
   `sha256:266271798fd56bf591346145b8256a67ee0d5ca57b63af1f760f61f67c532e10`.
   The legacy two-case identity remains
   `sha256:e4a782b32d69f7d7e7d25df60ef3416b4c950315e30d5cf716cd05fe2e0c4203`.
   These are **not** comparable as same-profile model quality scores.
5. **Content-free quality diagnostics.** Quality task reports can now include
   an optional `artifactReadStrategy` with counts: `queried`,
   `offsetOnly`, `candidatePages`, `normalizedHintPages`,
   `hintedOffsetPages`, `donePages`. The Host derives them from the
   actual successful authorized Handler request/response; no query text,
   Artifact ID, raw offset, candidate preview or source content is emitted.
   The Python reader strictly validates the sum of query+offset counts against
   `artifactReads`, bounds each count and checks candidate/hint subset
   relationships. Historical reports without this field remain valid and
   are never retrospectively assigned a breakdown.
6. **No causal shortcut.** Read-mode metrics show observed behavior, not
   whether a model, Tool, Context or Provider caused an ultimate failure.
   Terminal/failure attribution still comes from trusted execution and
   receipts, not the last HTTP 200 or a high output ceiling.

## RED → GREEN and systematic regressions

- A fresh unit test first reproduced the missing hint on a candidate-enabled,
  queryless long document. The initial implementation only added a field, which
  passed a small-page test but **FAILED** an actual Agent/Tool/Context
  end-to-end test using `limit=8000`: Context archived the large result,
  hiding the hint. The subsequent corrected implementation keeps the
  complete advised result within 512 bytes.
- A source-blind deterministic Agent starts with `offset=0,limit=8000`,
  reads the generic hint, searches the real source using `query`, selects
  the bounded `R-17` preview, re-reads exact original evidence and commits
  one draft. The actual same-Run recovery Oracle passes. This proves the
  *mechanism works for a model that reacts to the Tool hint*, not that a live
  `gpt-5.6-terra` run will use it.
- An 80-seed × 4-offset native Tool test traverses Unicode, serialized JSON,
  source boundaries and exact cursor correctness. Every enhanced long
  queryless result has <=512 bytes and the original prefix at its published
  source offsets. A small-document case proves no hint/compaction change.
- The original candidate literal/normalized-hint property suite and cross-
  session Artifact authority checks are retained; the default reader is not
  downgraded to automatic fuzzy search. Full offline 40/40 scripted
  calibration still uses real Tool/Context/Run and recovery paths.
- Python tests validate optional diagnostic counts, reject forged sums,
  extra raw query text, negative and Boolean counts and inconsistent hint
  subsets; old reports remain readable without invented metrics.

## Explicit limits and delivery gate

The only completed genuine paid 40-case score remains **37/40** on the
earlier profile, and the original first `distractor-05` pilot failed on
source `33c876b`. **No new live Provider requests or paid tasks were
executed** for this patch. The original early termination root cause remains
unproven. A later explicitly authorized `make agent-quality-distractor-pilot`
would test whether the model actually adapts to the advisory and records
query vs offset counts; it must retain its new dataset hash and bounded
one-case pilot restrictions.

Final commit, independent code boundary review, test/Race/lint, GitHub PR
Head and any required integration gates remain distinct delivery checks.
