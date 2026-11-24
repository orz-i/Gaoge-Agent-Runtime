package handoff

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RemoteProofMAC signs the complete immutable Kernel execution identity,
// protocol-specific shadow state, revision and projected result. The state
// must exclude its own Proof field. Distinct protocol domains and Host-owned
// keys prevent a Task or Session from impersonating another protocol.
// Signing is not an authorization: only an admitted original physical
// operation may call this helper.
func RemoteProofMAC(
	key []byte, domain string, snapshot kernel.Snapshot, state any,
	revision uint64, status kernel.RunStatus, result *kernel.Result,
	checkpoint *kernel.Checkpoint, errorCode, errorDetail string,
) ([]byte, error) {
	payload := struct {
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
		State       any
		Result      *kernel.Result
		Checkpoint  *kernel.Checkpoint
		ErrorCode   string
		ErrorDetail string
	}{
		Domain: domain, RunID: snapshot.Run.ID, Kind: snapshot.Run.Kind,
		Actor: snapshot.Run.Actor, Thread: snapshot.Run.Thread,
		RequestID: snapshot.Run.RequestID, Goal: snapshot.Run.Goal,
		ParentRunID: snapshot.Run.ParentRunID,
		Revision:    revision, Status: status, State: state,
		Result: result, Checkpoint: checkpoint, ErrorCode: errorCode,
		ErrorDetail: errorDetail,
	}
	wire, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(wire)
	return mac.Sum(nil), nil
}

// VerifyRemoteProof never accepts mutable Kernel snapshots or equal IDs
// without the original Host key and exact signed state, including terminal
// status, result and return/no-effect facts.
func VerifyRemoteProof(key []byte, domain string, snapshot kernel.Snapshot, state any, proof string) bool {
	if len(key) < 32 || strings.TrimSpace(domain) == "" || len(proof) != sha256.Size*2 {
		return false
	}
	actual, err := hex.DecodeString(proof)
	if err != nil {
		return false
	}
	expected, err := RemoteProofMAC(key, domain, snapshot, state,
		snapshot.Run.Revision, snapshot.Run.Status, snapshot.Result,
		snapshot.Checkpoint, snapshot.Run.ErrorCode, snapshot.Run.ErrorDetail)
	return err == nil && hmac.Equal(actual, expected)
}
