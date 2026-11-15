package a2a

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A Shadow is ordinary writable Kernel state, not a remotely authenticated
// receipt. The Host supplies a stable, private signing key across process
// restarts; it MUST NOT be exposed to other Features or stored in the Kernel.
// Every first-party remote observation seals the entire resulting state,
// terminal result and original Run identity. A same-ID Kernel twin cannot
// substitute a fabricated Task ID or a terminal status without that proof.
const shadowProofDomain = "a2a.shadow.lifecycle.v1"

type shadowProofInput struct {
	Domain      string
	RunID       string
	Kind        kernel.RunKind
	Actor       kernel.ActorRef
	Thread      kernel.ThreadRef
	RequestID   string
	Goal        string
	ParentRunID string
	Revision    uint64
	Status      kernel.RunStatus
	State       shadowState
	Result      *kernel.Result
	Checkpoint  *kernel.Checkpoint
	ErrorCode   string
	ErrorDetail string
}

func (runner *ShadowRunner) proofMAC(
	snapshot kernel.Snapshot, state shadowState, revision uint64,
	status kernel.RunStatus, result *kernel.Result, checkpoint *kernel.Checkpoint, errorCode, errorDetail string,
) ([]byte, error) {
	state.Proof = ""
	wire, err := json.Marshal(shadowProofInput{
		Domain: shadowProofDomain, RunID: snapshot.Run.ID, Kind: snapshot.Run.Kind,
		Actor: snapshot.Run.Actor, Thread: snapshot.Run.Thread,
		RequestID: snapshot.Run.RequestID, Goal: snapshot.Run.Goal,
		ParentRunID: snapshot.Run.ParentRunID,
		Revision:    revision, Status: status, State: state,
		Result: result, Checkpoint: checkpoint, ErrorCode: errorCode, ErrorDetail: errorDetail,
	})
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, runner.attestationKey)
	_, _ = mac.Write(wire)
	return mac.Sum(nil), nil
}

func (runner *ShadowRunner) verifiedProof(snapshot kernel.Snapshot, state shadowState) bool {
	if runner == nil || len(runner.attestationKey) < 32 || len(state.Proof) != sha256.Size*2 ||
		snapshot.Run.Kind != RunKind {
		return false
	}
	actual, err := hex.DecodeString(state.Proof)
	if err != nil {
		return false
	}
	expected, err := runner.proofMAC(snapshot, state, snapshot.Run.Revision,
		snapshot.Run.Status, snapshot.Result, snapshot.Checkpoint, snapshot.Run.ErrorCode, snapshot.Run.ErrorDetail)
	return err == nil && hmac.Equal(actual, expected)
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
