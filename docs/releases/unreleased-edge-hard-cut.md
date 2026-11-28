# Edge interface hard cut (development candidate)

- HTTP start/read/cancel share `RunSnapshot`; remove `CancelRunResponse`,
  `PendingToolResponse`, `PendingToolProjector` and Agent HTTP snapshot wrappers.
  Use `runtimehttp.SnapshotResponse` and `agent.PendingToolView`.
- Replace `CancellationRoute`/`CancellationRouter` with the existing owner
  canceller in `Dependencies.Canceller`. Remote Runs cannot fall back to local
  Kernel cancellation. No first-party route aliases remain to remove.
- A2A/ACP use `handoff.Config.Remote`, `RemoteShadowObserver`, `agent.remote`
  and revision-bound `CancelRemote`. A2A direct resume now takes a revision.
  Preserve exact protocol binding, remote IDs and signed outcome proofs.
- MCP `NewRegistry` returns `*tools.Registry`. Discovery has one `Endpoint`
  and one `tools.Definition` per Tool; use `Definition.Name`/`InputSchema`.
  `CatalogSnapshot`, its clone, the MCP Registry wrapper and obsolete
  `ErrLegacyProtocol` are removed. Negotiated legacy MCP still works.
- ACP Hosts must supply their product `HostDependencies.Implementation`.
- HTTP no longer manufactures terminal SSE IDs or cancellation Feed writes.
  Drained terminal Run feeds return 204; retained SSE/error/cursor shapes stay
  unchanged. The TS client stops on 204. Regenerate TS via `pnpm generate` and
  verify `pnpm contract:check`; Harness schemas are unchanged.

Gaoge-Agent-Server was inspected read-only: its product HTTP/TS API does not
consume these adapters. Its direct Runtime/Harness integration is unchanged.
Further merging of A2A/ACP physical intent/return/proof state needs a canonical
Runtime remote-operation transaction/receipt contract: the frozen interface
offers Kernel snapshots/Apply and Handoff observation/control, not such a
transaction. Protocol-specific shadow evidence is retained rather than adding
an edge facade or claiming atomic remote execution.
