package harnesspostgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

// The relational Harness Stores share this one Checkpoint persistence format.
// Go's json.Marshal rewrites json.RawMessage (HTML escaping and whitespace),
// even when the original Checkpoint is valid. An original Provider replay and
// its Tool call arguments are physical evidence: neither can be rewritten on
// disk while the public Checkpoint content hash remains valid.
//
// Keep ordinary typed Checkpoint fields in JSON and carry only these exact
// opaque bytes as base64. The typed projection uses null placeholders so the
// original bytes are stored once, not duplicated in the database. This is a
// private, pre-stable Store format, not a new Context hash or runtime codec.
const checkpointStorageFormat = "checkpoint-opaque-v1"

type checkpointStorage struct {
	Format     string                     `json:"format"`
	Checkpoint runtimecontext.Checkpoint  `json:"checkpoint"`
	Opaque     []checkpointOpaqueEvidence `json:"opaque"`
	OpaqueHash string                     `json:"opaqueHash"`
}

type checkpointOpaqueEvidence struct {
	Entry         int      `json:"entry"`
	ToolArguments [][]byte `json:"toolArguments,omitempty"`
	ProviderData  []byte   `json:"providerData"`
}

func marshalCheckpointStorage(original runtimecontext.Checkpoint) (string, error) {
	if !runtimecontext.ValidCheckpoint(original) {
		return "", harness.ErrInvalidRequest
	}
	value := checkpointStorage{Format: checkpointStorageFormat, Checkpoint: runtimecontext.CloneCheckpoint(original), Opaque: []checkpointOpaqueEvidence{}}
	for i := range value.Checkpoint.Window.Entries {
		message := &value.Checkpoint.Window.Entries[i].Message
		if len(message.ToolCalls) == 0 && message.ProviderReplay == nil {
			continue
		}
		evidence := checkpointOpaqueEvidence{Entry: i}
		if len(message.ToolCalls) > 0 {
			evidence.ToolArguments = make([][]byte, len(message.ToolCalls))
			for j := range message.ToolCalls {
				evidence.ToolArguments[j] = bytes.Clone(message.ToolCalls[j].Arguments)
				message.ToolCalls[j].Arguments = json.RawMessage("null")
			}
		}
		if message.ProviderReplay != nil {
			evidence.ProviderData = bytes.Clone(message.ProviderReplay.Data)
			message.ProviderReplay.Data = json.RawMessage("null")
		}
		value.Opaque = append(value.Opaque, evidence)
	}
	opaque, err := json.Marshal(value.Opaque)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(opaque)
	value.OpaqueHash = hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalCheckpointStorage(raw string) (runtimecontext.Checkpoint, error) {
	var saved checkpointStorage
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.Format != checkpointStorageFormat {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	opaque, err := json.Marshal(saved.Opaque)
	if err != nil {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	digest := sha256.Sum256(opaque)
	if saved.OpaqueHash != hex.EncodeToString(digest[:]) {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	cursor := 0
	for i := range saved.Checkpoint.Window.Entries {
		message := &saved.Checkpoint.Window.Entries[i].Message
		if len(message.ToolCalls) == 0 && message.ProviderReplay == nil {
			continue
		}
		if cursor >= len(saved.Opaque) || saved.Opaque[cursor].Entry != i {
			return runtimecontext.Checkpoint{}, harness.ErrConflict
		}
		evidence := saved.Opaque[cursor]
		cursor++
		if len(evidence.ToolArguments) != len(message.ToolCalls) || (message.ProviderReplay == nil && len(evidence.ProviderData) != 0) {
			return runtimecontext.Checkpoint{}, harness.ErrConflict
		}
		for j := range message.ToolCalls {
			if !bytes.Equal(message.ToolCalls[j].Arguments, []byte("null")) ||
				(len(evidence.ToolArguments[j]) != 0 && !json.Valid(evidence.ToolArguments[j])) {
				return runtimecontext.Checkpoint{}, harness.ErrConflict
			}
			message.ToolCalls[j].Arguments = json.RawMessage(bytes.Clone(evidence.ToolArguments[j]))
		}
		if message.ProviderReplay != nil {
			if !bytes.Equal(message.ProviderReplay.Data, []byte("null")) ||
				(len(evidence.ProviderData) != 0 && !json.Valid(evidence.ProviderData)) {
				return runtimecontext.Checkpoint{}, harness.ErrConflict
			}
			message.ProviderReplay.Data = json.RawMessage(bytes.Clone(evidence.ProviderData))
		}
	}
	if cursor != len(saved.Opaque) || !runtimecontext.ValidCheckpoint(saved.Checkpoint) {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	return runtimecontext.CloneCheckpoint(saved.Checkpoint), nil
}
