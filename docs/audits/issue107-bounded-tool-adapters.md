# Issue 107: explicit bounded first-party Tool composition

This source change is not a passing test or matrix acceptance record.

## Opt-in migration

- `harness.NewBoundedBudgetAdapter` now supplies Runtime-owned Run, Model and Tool orchestration. Put the returned concrete adapter in the relevant Agent middleware lists. The original Harness `BudgetMiddleware` may still supply Host budget operations and read-only receipt verification.
- `harness.NewBoundedToolTimelineAdapter` supplies Runtime-owned Tool timeline orchestration. Host authority lookup, Item storage and projection remain supplied by the same Harness ports.
- The ordinary `NewBudgetMiddleware` and `NewToolTimelineMiddleware` constructors remain available and opaque. This is explicit composition, not transparent conversion of arbitrary middleware into a trusted completion source.

Runtime controls admission, the actual `Next` call and bounded post-call operations. Budget retains its original reservation, dispatch, unknown suspension, exact rejected-no-effect receipt, charge and projection semantics. Timeline retains started, pending and terminal Item ordering. Host ports cannot call `Next` or mint whole-callback completion.

A Tool rejected-no-effect receipt proves the Tool outcome, not arbitrary middleware effects. An ordinary middleware returning the same recoverable error therefore remains unknown. The new capability fixture checks both the explicitly composed success path and the ordinary timeline's unknown/no-replay path.

## Other closed regression cases

Refresh checks the original cancellation fence before subtask or start-item repairs, including when safe cleanup leaves the root Running. Only the original Cancel path can finish its election.

The mixed-specialist success scenario now uses an authoritative rejected Model response (response ID and final usage) for its known failed specialist. A separate generic-provider-error sibling case remains pending, preserves the original parent scope and cannot trigger another provider call or fabricate a failed-child receipt.
