package openai

import (
	"bytes"
	"encoding/json"
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
// Responses phase and reasoning continuity with store=false.
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

// verifiedReplay allows only supported original phase/reasoning output items.
// It never interprets provider context as authority; every Function Call ID,
// name and argument must match the Agent's durable, already authorized
// transcript before the request is dispatched.
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
	projection, err := projectOutput(outputs)
	if err != nil {
		return nil, err
	}
	if !projection.phased && (!projection.reasoning || len(projection.calls) == 0) ||
		projection.phased && len(projection.calls) == 0 && projection.reasoning ||
		len(projection.calls) != len(message.ToolCalls) {
		return nil, ErrUnsupported
	}
	// Agent normalizes outer whitespace before storing either a tool step or a
	// final message. Compare that same projection without altering raw replay.
	if strings.TrimSpace(projection.content) != strings.TrimSpace(message.Content) {
		return nil, ErrUnsupported
	}
	// Intermediate provider messages, like encrypted reasoning, are context
	// evidence. Every function call still must match the committed transcript;
	// excluding commentary from final content cannot hide an executable call.
	for i, original := range projection.calls {
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
