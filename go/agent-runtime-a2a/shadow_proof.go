package a2a

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A Shadow is ordinary writable Kernel state, not a remotely authenticated
// receipt. The Host supplies a stable, private signing key across process
// restarts; it MUST NOT be exposed to other Features or stored in the Kernel.
// Every first-party remote observation seals the entire resulting state,
// terminal result and original Run identity. A same-ID Kernel twin cannot
// substitute a fabricated Task ID or a terminal status without that proof.
const shadowProofDomain = "a2a.shadow.lifecycle.v1"

func (runner *ShadowRunner) proofMAC(
	snapshot kernel.Snapshot, state shadowState, revision uint64,
	status kernel.RunStatus, result *kernel.Result, checkpoint *kernel.Checkpoint, errorCode, errorDetail string,
) ([]byte, error) {
	state.Proof = ""
	return handoff.RemoteProofMAC(runner.attestationKey, shadowProofDomain, snapshot,
		state, revision, status, result, checkpoint, errorCode, errorDetail)
}

func (runner *ShadowRunner) verifiedProof(snapshot kernel.Snapshot, state shadowState) bool {
	if runner == nil || snapshot.Run.Kind != RunKind {
		return false
	}
	proof := state.Proof
	state.Proof = ""
	return handoff.VerifyRemoteProof(runner.attestationKey, shadowProofDomain,
		snapshot, state, proof)
}

// Only first-party ShadowRunner transitions may create new attested snapshots.
// The MAC is computed for the *next* atomic Kernel revision. The proof is never
// a mutable identity claim supplied by a remote response or a generic Feature.
func (runner *ShadowRunner) applyAttested(
	ctx context.Context, snapshot kernel.Snapshot, state shadowState, mutation kernel.Mutation,
) (kernel.Snapshot, error) {
	if runner == nil || len(runner.attestationKey) < 32 || snapshot.Run.Kind != RunKind {
		return kernel.Snapshot{}, ErrRemoteBindingChanged
	}
	// Kernel canonicalizes these fields in the same Apply CAS. Attest the
	// durable representation, not untrimmed provider error text.
	mutation.ErrorCode = strings.TrimSpace(mutation.ErrorCode)
	mutation.ErrorDetail = strings.TrimSpace(mutation.ErrorDetail)
	proof, err := runner.proofMAC(snapshot, state, snapshot.Run.Revision+1,
		mutation.Status, mutation.Result, mutation.Checkpoint, mutation.ErrorCode, mutation.ErrorDetail)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Proof = hex.EncodeToString(proof)
	mutation.State, err = json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, mutation)
}
