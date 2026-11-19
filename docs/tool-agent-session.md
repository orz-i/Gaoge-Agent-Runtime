# Recommended bounded tool session

Use `harness.OpenToolAgentSession` for a single Agent with local Tools, bounded
Context, exact archive retrieval and stable Host Turn IDs. It composes the
existing runners; it is not another execution engine.

```go
session, err := harness.OpenToolAgentSession(harness.ToolAgentSessionOptions{
    Runtime: runtime, Store: harnessStore, Model: client,
    Tools: []harness.SessionTool{{
        Registration: searchRegistration,
        Policy: harness.ToolPolicySnapshot{
            DefinitionVersion: "search-v1", ApprovalMode: "never", SideEffectLevel: "read",
        },
    }},
    ResolveModelWindow: resolveVerifiedModelWindow,
    Session: harness.SessionOptions{
        HostThread: tenantScopedHostThread,
        Actor: authenticatedActor, Thread: tenantScopedKernelThread,
        Config: harness.ConfigSnapshot{
            Model: selectedModel,
            ContextPolicy: runtimecontext.Policy{MaxInputTokens: 16384},
        },
        Transcript: hostTranscript,
    },
})
if err != nil { return err }
turn, err := session.Prompt(ctx, stableTurnID, "Find the original release evidence")
```

The Host still explicitly constructs the Kernel Runtime and both Stores, selects
the model, authenticates the principal, persists conversation messages, and
implements the search handler. `TranscriptHistoryFunc` is the pure-text history
adapter; `ResolveModelWindow` must return the actual routed model's verified
capacity with reserved output space. Unknown capacity fails before model dispatch.
An optional `Counter` supplies the existing exact-token interface; without one
the existing conservative token/byte assessment applies.

The facade pairs each registration with its policy, derives the Tool keys once,
and installs the same exact `harness.read_context_artifact` registration/policy
and bounded preparation/rehydration used by explicit composition. Do not supply
`Config.ToolKeys` or `Config.ToolPolicies` as a second source of truth. It copies
the configuration and leaves the caller's options unchanged.

## Deliberate defaults and limits

- Host-declared nonterminal **read** tools opt into bounded different-argument
  exploration. **Write** tools do not. A read label is a Host attestation, not
  proof that arbitrary handler code has no side effects.
- `ApprovalMode: "never"` must be explicit. There is no hidden approval handler;
  a tool requiring approval is rejected during construction. Use the full
  runners for approvals, delegation, shared budgets, Skills, custom memory or
  arbitrary middleware. Unsupported features are not silently ignored.
- Agent call limits come from `Session.Config.Limits` and otherwise retain the
  existing bounded defaults. No budget is automatically raised.
- Model-window `ReservedOutputTokens` reserves input headroom; it is not a
  Provider output-token ceiling. If the Host requires an explicit per-request
  output ceiling or custom model-option policy, use the existing full
  composition rather than assuming this facade enforces one.
- The facade uses synchronous bounded Context preparation, **not** the separately
  opted-in managed Host-transfer recovery protocol. It does not bind or replace
  the caller's Kernel or claim additional callback settlement authority.
- `Resume` restores committed Context and the existing Agent Run. It does not
  rerun the Host transcript or blindly retry an unknown external effect.
- Reopen with the same Stores/identity/configuration and `Transcript: nil` for
  read/recovery only. Use `Load` for older answers after Host history advances.
- Memory Stores do not survive process exit. Persistent adapters, original
  Provider binding and Host process lifecycle remain explicit deployment work.

For the smallest text-only path, keep `OpenTextAgentSession`. For advanced
composition and fault-injected recovery, keep the
[task-agent example](../go/agent-runtime-harness/examples/task-agent/README.md).

Focused offline checks:

```sh
go test ./go/agent-runtime-harness -run '^TestToolAgentSession' -count=1
```

These tests include a complete search task after three no-match results, exact
deep-evidence archive retrieval, stable Turn replay, terminal recovery and
fail-closed unsupported configuration. They verify mechanics, not real-model
reasoning quality.
