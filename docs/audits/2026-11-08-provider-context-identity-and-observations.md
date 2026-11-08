# 2026-11-08 — Provider/Context count identity and quality reporting review

## Review scope and outcome

This second-pass code review examined the actual locked candidate
`d59e04ded7444538eb349d702c08f0e605744e7a` in the isolated
`governance/provider-context-evidence-20261107` worktree.

**One actionable validation gap was confirmed:** the Host wired the optional
native `model.InputTokenCounter` and recorded a dataset profile, but no
task-level evidence indicated whether the counter was actually called and
returned a native count or failed into the existing conservative Context
fallback. An HTTP total cannot be used as a substitute for this proof because
counting and generation requests are distinct, and intermediate requests may
be rejected before dispatch. This was **insufficient observability**, not a
proven undercount or a justified change to the Context budget.

The minimal remediation adds `inputCountObservations` **only to the OpenAI
quality Host's emitted sample**. It counts each attempted call into the
already-configured counter, a positive returned count or an unavailable/error
outcome. It does not change the Provider count request, ModelInvocation,
Run/Turn/Tool receipt, Context assessment, retry semantics or persisted
authority. It records no query, raw Artifact text, tokenized prompt, endpoint,
response ID or API key. An accepted native count is not a billing receipt or
a guarantee of actual generation; an unavailable count does not authorize
a cheaper/higher Context input limit.

## Independent invariant matrix (review against the entire affected path)

| Boundary | Invariant | Evidence and negative cases |
| --- | --- | --- |
| Host `model.Request` -> Router -> native Adapter | Exact Provider/model binding is preserved and only a Host-certified native input projection can be counted | Cross-model/protocol/alias/malformed JSON before HTTP tests, encrypted reasoning replay and Tool ID/argument integrity |
| Provider input count -> Context | Positive native count is not blindly trusted as a receipt or Model charge; errors preserve conservative Context hard-byte bound | Loopback positive count and unsupported path, invalid, duplicate, zero, fractional, huge and redirect responses; no automatic generation retry |
| Count observer -> quality report | `attempts=accepted+unavailable`; no raw request, error text, model source, IDs or count-as-usage assumptions | Host records state under its existing mutex, independently of the native router; old reports stay optional; Python rejects cross-provider/two-case spoofing, negative/Boolean/fractional/extra keys, mismatched totals |
| Artifact lookup -> Run/Turn/Session -> Source | Every hint follows original Artifact identity, scope and hash verification; no new authorization | Existing foreign-session and cloned-tampered-store tests, small/large candidate tests, 80-seed Unicode paging source exactness |
| Tool result -> Model navigation | On large enhanced queryless reads, data is a bounded exact source prefix + content-free hint, never a hidden model answer | RED small-page test and RED actual 8000-rune Agent path; GREEN <512-byte hint, query, exact read, one draft, same-Run recovery |
| Agent interruption -> durable receipts | Only the local committed draft and pure interruption sentinel permit recovery; no recomputation on failed/rejected model response | PR #97 typed-failure/inconsistent-receipt matrix, same-Run/Turn/lease checks, unknown failures preserved |
| Quality report -> comparison | No retroactive change to the original 37/40 measured history or failed `distractor-05` sample. Output ceiling != consumed Tokens. New Tool contract rotates dataset hash, report-only observation does not | Pinned historical SHA 562c... (40 cases), failed pilot SHA 12a851... (one case), model ceiling 128k vs 4096 same 32768 input limit, old score not edited |

All repository files above are public-facing code/docs. The private paid
report used for read-only audit was not re-serialized or committed.

## Results and constraints

- Go Provider and Harness/test Agent fixture tests include valid native count
  acceptance and an unsupported custom endpoint **with no fake success**.
  Counting observations are aggregated across Host pre-interruption and
  recovery composition without a second metadata registry.
- The Python report protocol tests exercise the optional observational
  receipt, source identity, historical-compatibility and malformed-metadata
  matrix. They do not accept credentials, raw queries or arbitrary strings as
  metrics.
- The 40-case offline scripted corpus exercises genuine Agent/Context/Tool
  execution and recovery; its exact pinned hash remains
  `sha256:266271798fd56bf591346145b8256a67ee0d5ca57b63af1f760f61f67c532e10`.
  The updated OpenAI Tool/Context profile hash stays
  `sha256:7905b6abca186865869bcdc89ad8178cc2cb2ec4b5b1ada64aea4356ec97f992`.
  These are distinct from previous paid measured profiles.
- A single long `make check` invocation was interrupted by the remote
  command connection during Go Race. Split module commands are used for
  explicit verification; a partial log cannot be reported as a complete gate.
- An independent separate-model reviewer was not available locally
  (Codex CLI was not logged in; native Pi external auth was unavailable).
  This review is a separate code/test pass, **not** a claim of an independently
  authenticated reviewer. GitHub CodeQL and mandatory PR-Head checks, when
  available, are separate acceptance evidence.

## Exit status

**Locally testable authority and count-observation invariants:** subject to
final locked HEAD tests and repository CI. **External validation blockers:**
real native OpenAI input-token response, exact-vs-Provider Usage verification,
actual `gpt-5.6-terra` retrieval success and runtime/cost impact are all
still unmeasured on this new commit. A real Provider test must be invoked
explicitly by the operator's terminal with separately supplied trusted model
capability profile and hidden key; this review performs no billable requests.

The source checkout `main` and its two user-owned untracked Provider scripts
must not be modified during delivery.
