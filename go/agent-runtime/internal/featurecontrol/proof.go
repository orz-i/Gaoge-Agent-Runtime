// Package featurecontrol holds a small, in-process Feature-issued proof for
// guarded Run controls. Go's internal import rule prevents downstream SDK
// consumers and independent Store modules from minting a proof: only trusted
// Feature implementations inside agent-runtime can seal a validated decision.
// This is an SDK API boundary, NOT host authentication or a network credential.
package featurecontrol

import (
	"crypto/sha256"
	"encoding/json"
)

type Proof struct {
	runID    string
	revision uint64
	kind     string
	source   string
	hash     [32]byte
}

// Seal is callable only by first-party packages inside the Go module's
// internal boundary. Call it AFTER the Feature validates the checkpoint,
// decision, budget hard ceiling and actor authorization.
func Seal(runID string, revision uint64, kind, source string,
	state json.RawMessage, checkpoint any) *Proof {
	if runID == "" || revision == 0 || kind == "" || source == "" || !json.Valid(state) {
		return nil
	}
	hash, ok := digest(state, checkpoint)
	if !ok {
		return nil
	}
	return &Proof{runID: runID, revision: revision, kind: kind, source: source, hash: hash}
}

// Matches binds exactly one validated state and Checkpoint mutation to the
// origin gate and revision. Replaying it at the next Run revision fails.
func (proof *Proof) Matches(runID string, revision uint64, kind, source string,
	state json.RawMessage, checkpoint any) bool {
	if proof == nil || proof.runID != runID || proof.revision != revision ||
		proof.kind != kind || proof.source != source {
		return false
	}
	hash, ok := digest(state, checkpoint)
	return ok && hash == proof.hash
}

func digest(state json.RawMessage, checkpoint any) ([32]byte, bool) {
	encoded, err := json.Marshal(struct {
		State      json.RawMessage `json:"state"`
		Checkpoint any             `json:"checkpoint"`
	}{State: state, Checkpoint: checkpoint})
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(encoded), true
}
