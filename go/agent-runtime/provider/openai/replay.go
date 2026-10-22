package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	replayFormat   = "responses.output.v1"
	maxReplayBytes = 8 << 20
	maxReplayItems = 1024
)

// encodeReplay stores provider-authored output items in their original order.
// The ordinary Agent transcript remains the authority for user-visible
// messages and executable Tool calls. This evidence is only for stateless
// Responses reasoning continuity with store=false.
func encodeReplay(output []json.RawMessage) (*model.ProviderReplay, error) {
	if len(output) == 0 || len(output) > maxReplayItems {
		return nil, ErrUnsupported
	}
	data, err := json.Marshal(output)
	if err != nil || len(data) > maxReplayBytes {
		return nil, ErrUnsupported
	}
	return &model.ProviderReplay{
		Provider: "openai", Format: replayFormat, Data: data,
	}, nil
}

// verifiedReplay allows only the original OpenAI reasoning/function-call
// output items to be returned. It never interprets encrypted reasoning as
// authority; every Function Call ID, name and argument must match the Agent's
// durable, already authorized transcript before the request is dispatched.
func verifiedReplay(message model.Message) ([]json.RawMessage, error) {
	replay := message.ProviderReplay
	if replay == nil || replay.Provider != "openai" || replay.Format != replayFormat ||
		len(replay.Data) == 0 || len(replay.Data) > maxReplayBytes {
		return nil, ErrUnsupported
	}
	var outputs []json.RawMessage
	if err := json.Unmarshal(replay.Data, &outputs); err != nil ||
		len(outputs) == 0 || len(outputs) > maxReplayItems {
		return nil, ErrUnsupported
	}
	var text strings.Builder
	calls := make([]outputItem, 0, len(message.ToolCalls))
	seenReasoning := false
	for _, raw := range outputs {
		var item outputItem
		if len(raw) == 0 || json.Unmarshal(raw, &item) != nil {
			return nil, ErrUnsupported
		}
		switch item.Type {
		case "reasoning":
			if item.EncryptedContent == "" {
				return nil, errors.Join(ErrUnsupported, ErrReplayUnavailable)
			}
			seenReasoning = true
		case "message":
			// The original message is replayed exactly, including any phase
			// markers. Only known textual content types are eligible.
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					text.WriteString(part.Text)
				case "refusal":
					if part.Refusal == "" {
						return nil, ErrUnsupported
					}
					text.WriteString(part.Refusal)
				default:
					return nil, ErrUnsupported
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return nil, ErrUnsupported
			}
			calls = append(calls, item)
		default:
			return nil, ErrUnsupported
		}
	}
	if !seenReasoning || len(calls) == 0 || len(calls) != len(message.ToolCalls) ||
		strings.TrimSpace(text.String()) != strings.TrimSpace(message.Content) {
		return nil, ErrUnsupported
	}
	for i, original := range calls {
		approved := message.ToolCalls[i]
		if approved.ToolKey == "" || original.CallID != approved.ID || original.Name != approved.Name {
			return nil, ErrUnsupported
		}
		var left, right bytes.Buffer
		if json.Compact(&left, []byte(original.Arguments)) != nil ||
			json.Compact(&right, approved.Arguments) != nil ||
			!bytes.Equal(left.Bytes(), right.Bytes()) {
			return nil, ErrUnsupported
		}
	}
	// Return an isolated slice; no downstream serializer may mutate
	// authoritative replay evidence stored in the Agent's Feature state.
	cloned := make([]json.RawMessage, len(outputs))
	for i := range outputs {
		cloned[i] = append(json.RawMessage(nil), outputs[i]...)
	}
	return cloned, nil
}
