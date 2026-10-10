# ADR 0007: Classified Model Failures and Least-Privilege Error Diagnostics (P2)

- Status: Accepted for candidate implementation; **not released**
- Date: 2026-10-11
- Base: `origin/main@1669ffdc`, released SDK `0.1.0-beta.16`
- Host design: Gaoge ADR-0113; no host SDK pin upgrade before an audited, published complete beta cohort

## Problem and evidence

`agent.Runner` currently persists `agent.model_failed` or `agent.model_retry_exhausted` after collapsing the host model adapter's `*UpstreamError` classification. By contrast, the durable invocation's execution attempt and retryability are already owned by Agent; changing the end-user retry button or adding a host retry loop cannot restore the lost cause. The SDK public HTTP projections contain unreviewed free-text `Run.ErrorDetail`, `Turn.ErrorDetail`, invocation/subtask `ErrorDetail`, Workbench overview, run/harness feed `Message` and kernel journal events. Object ownership alone does not confer debugging privileges.

The Kernel **must retain** internal diagnostic details for authorized engineering investigation and durable integrity. However, public HTTP exposure is a separate decision. Only known bounded codes and static safe messages may be projected to ordinary authenticated consumers, with host-owned authorization for any later diagnostic read path.

## Decision

1. **Provider-neutral typed cause**: `model` owns an opt-in wrapped `ModelFailureCode() string` contract with `NewClassifiedError(cause, code)` / `ClassifiedErrorCode(err)`. Only exact supported, low-cardinality error codes pass SDK allowlist; unknown strings, arbitrary Provider body and secrets fail closed. `errors.As/Unwrap` preserve cause and independent `RetryableError` semantics. No new background retry, no new persistent state, and no provider-specific dependencies inside Agent, Kernel, Harness.
2. **Durable terminal code**: Agent prefers the typed code **only** at the point where an actual model invocation is irrecoverably terminal (non-retryable failure or exhausted bounded attempts). Otherwise retains the existing `agent.model_failed` / `agent.model_retry_exhausted` generic codes. Pause/approval/cancel/deadline policies and usage accounting retain their existing precedence and identity. The root error's detail remains internally recorded, never used as a string-parsing classifier. Do not change the logical attempt ID or replay policy.
3. **Least-privilege projection**: SDK HTTP reads return bounded code and an invariant safe error summary for model/run/harness failure diagnostics; do not return arbitrary `ErrorDetail`, raw error `Message` from terminal feed events, or raw `ErrorPreview`. Scrub ordinary Run/Cancel responses, Workbench overview, Harness Turn/Invocation/Subtask and feed projections **at the server-side adapter**; do not rely on CSS, frontend hiding, or client-side regex. Preserve legitimate authored goal, tool args/output and result content as separate user data under existing object authorization; they are not administrator errors. Review Workbench nested diagnostic fields explicitly; if not fully projected, treat P2 as NO-GO.
4. **Authorization semantics**: Owner/tenant authorization for ordinary Run/Harness HTTP remains mandatory. A run owner must not automatically receive full internal `ErrorDetail`. No query param or custom client header may upgrade diagnostics. A separate admin/debug diagnostic endpoint requires host-supplied explicit object-level and role-level authorization and an audit mechanism, **never** the default owner authorizer. Full Admin incident workbench is P3; SDK should not invent Gaoge-specific RBAC.
5. **Host contract**: Host Gateway keeps structured HTTP status/error-type/request ID as diagnostic facts. Gaoge's Conversation P1 maps recognized typed SDK error code to its canonical `errorResolution` and i18n; host may construct `model.NewClassifiedError` only after verified typed classification, not by searching provider free text. Distinguish Gateway route missing, access denied, entitlement denied; do not conflate provider HTTP 403 with platform model rights.
6. **Release and hard cut**: SDK changes require an independently audited new Beta and packed Go + TypeScript consumer validation; Gaoge must keep the published beta.16 pin until that release is explicitly authorized and available. Do not smuggle local SDK source into the host production pin, add a compatibility bridge, or rewrite historical Runs.
7. **Observability**: existing OTel `error.type` may record a bounded classified code, not Provider message/body or high-cardinality fields. Request ID/trace linkage and admin diagnostic read controls require P2 host authorization; actual attempt-vs-ledger observability and administrator investigation UI belong to P3.

## Negative acceptance cases

- Provider error text containing `401`, `rate_limit`, `sk-secret` or `temporary` must not determine code; unknown codes fall back to generic.
- 429 rate limit does not mean permanent quota; 401/403 credentials vs platform model access retain distinct codes. The host, not the SDK, translates Provider response types.
- Model invocation remains bounded/receipt-safe; retry exhaustion can preserve a typed reason without reissuing extra calls.
- Public GET Run / Cancel / Workbench / Harness Turn / Subtask / Invocation / terminal feed / event journal never expose `Bearer sk-...` seeded in raw error details; owner and unauthenticated users cannot bypass that through query params.
- Cancelled/paused/waiting and successful Runs do not acquire a failure recovery status; Runtime Usage remains a ledger.
- SDK Go race, HTTP wire/fixture contract, packed TypeScript consumer, integration and `make check` must pass. If one endpoint remains raw, mark explicit blocker.

## Alternative and consequences

A host-only error-mapping table or text-regex parser was rejected: once the Runner writes `agent.model_failed`, structured cause is lost. Client-side suppression was rejected because sensitive bytes still cross the API. Exposing the raw detail to every owner was rejected because execution ownership and debugging privilege differ. This changes SDK HTTP failure-detail semantics and introduces a typed model wrapper: both require a new beta release and coordinated host adoption. Existing persistence is unchanged; historic generic failures remain generic.