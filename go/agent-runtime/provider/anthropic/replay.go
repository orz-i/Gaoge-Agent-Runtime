package anthropic

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const replayFormat = "messages.content.v1"

func forcedChoiceUnsupported(name string) bool {
	// These models reject tool_choice=any/tool even with adaptive thinking.
	// This list is deliberately restrictive and should be revised with the
	// official model capability matrix; unknown models rely on the API.
	for _, prefix := range []string{
		"claude-opus-5-5", "claude-sonnet-5-5",
		"claude-fable-5-1", "claude-mythos-5-1",
	} {
		if name == prefix || strings.HasPrefix(name, prefix+"-") {
			return true
		}
	}
	return false
}

// verifiedReplay allows an Anthropic continuation to resend the exact
// signed assistant blocks (including thinking/redacted thinking) after a Tool
// call. Verify they still match the Agent-validated visible transcript and
// Tool intents; opaque provider bytes can never grant new capabilities.
func verifiedReplay(message model.Message) (json.RawMessage, error) {
	replay := message.ProviderReplay
	if replay == nil || replay.Provider != "anthropic" || replay.Format != replayFormat ||
		len(replay.Data) == 0 || len(replay.Data) > 16<<20 {
		return nil, ErrUnsupported
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(replay.Data, &blocks); err != nil || len(blocks) == 0 {
		return nil, ErrUnsupported
	}
	var text strings.Builder
	var calls []contentBlock
	for _, raw := range blocks {
		block, err := parseContentBlock(raw)
		if err != nil {
			return nil, err
		}
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if block.ID == "" || block.Name == "" || !json.Valid(block.Input) {
				return nil, ErrUnsupported
			}
			calls = append(calls, block)
		case "thinking":
			if block.Signature == "" {
				return nil, ErrUnsupported
			}
		case "redacted_thinking":
			if block.Data == "" {
				return nil, ErrUnsupported
			}
		default:
			return nil, ErrUnsupported
		}
	}
	if strings.TrimSpace(text.String()) != strings.TrimSpace(message.Content) ||
		len(calls) != len(message.ToolCalls) || len(calls) == 0 {
		return nil, ErrUnsupported
	}
	for i, previous := range calls {
		current := message.ToolCalls[i]
		if previous.ID != current.ID || previous.Name != current.Name || current.ToolKey == "" {
			return nil, ErrUnsupported
		}
		var left, right bytes.Buffer
		if json.Compact(&left, previous.Input) != nil || json.Compact(&right, current.Arguments) != nil ||
			!bytes.Equal(left.Bytes(), right.Bytes()) {
			return nil, ErrUnsupported
		}
	}
	return append(json.RawMessage(nil), replay.Data...), nil
}

// marshalStreamBlock reconstructs each known Anthropic content block without
// discarding the empty thinking field required alongside a signed omission.
// No unrecognized block type is serialized into a durable replay payload.
func marshalStreamBlock(block *contentBlock) (json.RawMessage, error) {
	if block == nil {
		return nil, ErrUnsupported
	}
	var output map[string]any
	switch block.Type {
	case "text":
		output = map[string]any{"type": "text", "text": block.Text}
	case "tool_use":
		if block.Name == "" || block.ID == "" || !json.Valid(block.Input) {
			return nil, ErrUnsupported
		}
		output = map[string]any{"type": "tool_use", "id": block.ID,
			"name": block.Name, "input": block.Input}
	case "thinking":
		if block.Signature == "" {
			return nil, ErrUnsupported
		}
		output = map[string]any{"type": "thinking", "thinking": block.Thinking, "signature": block.Signature}
	case "redacted_thinking":
		if block.Data == "" {
			return nil, ErrUnsupported
		}
		output = map[string]any{"type": "redacted_thinking", "data": block.Data}
	default:
		return nil, ErrUnsupported
	}
	return json.Marshal(output)
}

// parseContentBlock rejects unknown fields instead of reserializing a signed
// provider block while silently losing fields needed for future continuation.
func parseContentBlock(raw json.RawMessage) (contentBlock, error) {
	var fields map[string]json.RawMessage
	var block contentBlock
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &block) != nil {
		return contentBlock{}, ErrUnsupported
	}
	allowed := map[string]bool{"type": true}
	switch block.Type {
	case "text":
		allowed["text"] = true
	case "tool_use":
		allowed["id"], allowed["name"], allowed["input"] = true, true, true
	case "thinking":
		allowed["thinking"], allowed["signature"] = true, true
	case "redacted_thinking":
		allowed["data"] = true
	default:
		return contentBlock{}, ErrUnsupported
	}
	for field := range fields {
		if !allowed[field] {
			return contentBlock{}, ErrUnsupported
		}
	}
	return block, nil
}
