package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// sealStartAdmission freezes the entire Harness admission *before* the first
// durable Turn write. The Config ID embeds this digest through ContentHash,
// allowing existing CreateTurn identity CAS to reject a changed replay even
// if a crash happened before the root Invocation or Context checkpoint existed.
//
// Context bodies are hashed, never stored in ConfigSnapshot. Host message
// identities are stored separately because a crashed Item projection must be
// reconstructible without consulting a mutable Host transcript.
// rootAdmission binds the immutable root Agent input before CreateTurn.
// The Host never chooses a runtime capability interpreter.
type rootAdmission struct {
	InputHash string `json:"inputHash"`
}

func sealStartAdmission(request StartRequest, config *ConfigSnapshot, root rootAdmission) (StartRequest, error) {
	if config == nil || jsoncontract.ValidateCanonicalUTF8(request) != nil ||
		jsoncontract.ValidateCanonicalUTF8(*config) != nil || jsoncontract.ValidateCanonicalUTF8(root) != nil {
		return StartRequest{}, ErrInvalidRequest
	}
	root.InputHash = strings.TrimSpace(root.InputHash)
	var contextSeed *ContextSeed
	if request.Context != nil {
		normalized, err := normalizeContextSeed(request.Context)
		if err != nil {
			return StartRequest{}, err
		}
		contextSeed = normalized
	}
	input, err := cloneAdmissionHostRef(request.InputMessage)
	if err != nil {
		return StartRequest{}, err
	}
	output, err := cloneAdmissionHostRef(request.OutputMessage)
	if err != nil {
		return StartRequest{}, err
	}
	request.Context = contextSeed
	request.InputMessage = input
	request.OutputMessage = output
	request.RequiredToolKeys = normalizeStrings(request.RequiredToolKeys)
	request.InitialToolKeys = normalizeStrings(request.InitialToolKeys)
	request.CallAllowance = request.CallAllowance.Clone()
	// The caller cannot supply or retain stale admission facts. Always bind
	// the normalized, authorized request at this exact construction boundary.
	config.InputMessage = input
	config.OutputMessage = output
	payload := struct {
		Root             rootAdmission        `json:"root"`
		HostThread       HostRef              `json:"hostThread"`
		HostTurn         HostRef              `json:"hostTurn"`
		Actor            kernel.ActorRef      `json:"actor"`
		Thread           kernel.ThreadRef     `json:"thread"`
		RequestID        string               `json:"requestID"`
		Goal             string               `json:"goal"`
		RequiredToolKeys []string             `json:"requiredToolKeys"`
		InitialToolKeys  []string             `json:"initialToolKeys"`
		InputMessage     *HostRef             `json:"inputMessage"`
		OutputMessage    *HostRef             `json:"outputMessage"`
		Context          *ContextSeed         `json:"context"`
		CallAllowance    *agent.CallAllowance `json:"callAllowance"`
	}{
		Root: root, HostThread: request.HostThread, HostTurn: request.HostTurn,
		Actor: request.Actor, Thread: request.Thread,
		RequestID:        strings.TrimSpace(request.RequestID),
		Goal:             strings.TrimSpace(request.Goal),
		RequiredToolKeys: normalizeStrings(request.RequiredToolKeys),
		InitialToolKeys:  normalizeStrings(request.InitialToolKeys),
		InputMessage:     input, OutputMessage: output,
		Context:       contextSeed,
		CallAllowance: request.CallAllowance,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return StartRequest{}, ErrInvalidRequest
	}
	sum := sha256.Sum256(raw)
	config.AdmissionHash = hex.EncodeToString(sum[:])
	return request, nil
}

// requireFrozenAdmission refuses dispatch from a pre-hard-cut Turn whose
// original Context/authority was never bound to its immutable Config ID.
// Read-only inspection and fact reconciliation remain available during drain.
func (runner *Runner) requireFrozenAdmission(ctx context.Context, turn Turn) error {
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil {
		return err
	}
	if len(config.AdmissionHash) != 64 || config.TurnID != turn.ID {
		return ErrConflict
	}
	if _, err := hex.DecodeString(config.AdmissionHash); err != nil {
		return ErrConflict
	}
	return nil
}

func cloneAdmissionHostRef(ref *HostRef) (*HostRef, error) {
	if ref == nil {
		return nil, nil
	}
	normalized, err := normalizeHostRef(*ref)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}
