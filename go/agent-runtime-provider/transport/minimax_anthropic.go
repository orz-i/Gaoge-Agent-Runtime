package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Preserve native thinking signatures and server-tool blocks alongside text.
func applyAnthropicReplayRequest(protocol string, payload map[string]interface{}, input GenerateInput) {
	protocol = NormalizeAdapter(protocol)
	if protocol != AdapterMiniMaxAnthropic && protocol != AdapterAnthropicMessages {
		return
	}
	if thinking := asMap(payload[valueThinking0F938ED2]); protocol == AdapterMiniMaxAnthropic && getString(thinking[valueTypeF2B37301]) == valueEnabledD8AC0A8D {
		payload[valueThinking0F938ED2] = map[string]interface{}{valueTypeF2B37301: valueAdaptive8B08E1F4}
	}
	messages, _ := payload[valueMessages8CBF9CA9].([]map[string]interface{})
	index := 0
	for _, message := range normalizeMessages(input.Messages) {
		if message.Role == valueSystem730C6E5F {
			continue
		}
		if message.Role == valueAssistantE3AFA514 && index < len(messages) {
			if blocks := decodeProviderReplayItems(message.ReplayStateJSON, protocol); len(blocks) > 0 {
				messages[index][valueContent3EAD52FD] = blocks
			}
		}
		index++
	}
}

func recordAnthropicResponseReplay(protocol string, output *GenerateOutput, body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var parsed map[string]interface{}
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	blocks := asSlice(parsed[valueContent3EAD52FD])
	output.ReplayStateJSON = encodeProviderReplayItems(NormalizeAdapter(protocol), blocks)
	for _, raw := range blocks {
		block := asMap(raw)
		if getString(block[valueTypeF2B37301]) == valueToolUse6527A599 {
			for index := range output.ToolCalls {
				if output.ToolCalls[index].ToolCallID == getString(block["id"]) {
					output.ToolCalls[index].ArgumentsJSON = normalizeJSONString(block[valueInputF8378501])
				}
			}
		}
	}
	return nil
}

// A paused native call may return its result in the next model response without
// repeating server_tool_use. Recover only its still-pending identity from the
// exact input replay, so that execution is metered once and remains observable.
func reconcileAnthropicPendingResults(protocol string, input GenerateInput, output *GenerateOutput) {
	pending := pendingAnthropicServerCalls(protocol, input)
	indexByID := map[string]int{}
	for index, call := range output.ServerToolCalls {
		indexByID[call.ToolCallID] = index
	}
	for _, block := range decodeProviderReplayItems(output.ReplayStateJSON, protocol) {
		if !isAnthropicServerToolResultBlockType(getString(block[valueTypeF2B37301])) {
			continue
		}
		id := getString(block["tool_use_id"])
		call, found := pending[id]
		if _, exists := indexByID[id]; !exists {
			if !found {
				continue
			}
			indexByID[id] = len(output.ServerToolCalls)
			output.ServerToolCalls = append(output.ServerToolCalls, call)
		}
		applyAnthropicServerToolResult(output.ServerToolCalls, indexByID, block)
	}
}

const anthropicServerToolUseType = "server_tool_use"

func pendingAnthropicServerCalls(protocol string, input GenerateInput) map[string]ToolCall {
	pending := map[string]ToolCall{}
	for _, message := range input.Messages {
		for _, block := range decodeProviderReplayItems(message.ReplayStateJSON, protocol) {
			switch getString(block[valueTypeF2B37301]) {
			case anthropicServerToolUseType:
				call := anthropicServerToolUseCall(block)
				pending[call.ToolCallID] = call
			default:
				if isAnthropicServerToolResultBlockType(getString(block[valueTypeF2B37301])) {
					delete(pending, getString(block["tool_use_id"]))
				}
			}
		}
	}
	return pending
}

type anthropicReplay struct {
	protocol string
	blocks   map[int]map[string]interface{}
	inputs   map[int]string
}

func newAnthropicReplay(protocol string) *anthropicReplay {
	protocol = NormalizeAdapter(protocol)
	if protocol != AdapterMiniMaxAnthropic && protocol != AdapterAnthropicMessages {
		return nil
	}
	return &anthropicReplay{protocol: protocol, blocks: map[int]map[string]interface{}{}, inputs: map[int]string{}}
}

func (r *anthropicReplay) consume(event map[string]interface{}) error {
	index := anthropicContentBlockIndex(event)
	switch getString(event[valueTypeF2B37301]) {
	case "content_block_start":
		r.blocks[index] = cloneMap(asMap(event["content_block"]))
	case "content_block_delta":
		return r.consumeDelta(index, asMap(event[interactionKeyDelta]))
	case "content_block_stop":
		return r.finishInput(index)
	case anthropicEventMessageStop:
		if len(r.inputs) > 0 {
			return fmt.Errorf("%w: MiniMax Anthropic stream ended with unfinished tool input", errMiniMaxInvalidResponse)
		}
	}
	return nil
}

func (r *anthropicReplay) consumeDelta(index int, delta map[string]interface{}) error {
	block := r.blocks[index]
	if len(block) == 0 {
		return fmt.Errorf("%w: MiniMax Anthropic content delta without block %d", errMiniMaxInvalidResponse, index)
	}
	switch getString(delta[valueTypeF2B37301]) {
	case "text_delta":
		block[valueText53ED4E15] = getString(block[valueText53ED4E15]) + getString(delta[valueText53ED4E15])
	case "thinking_delta":
		block[valueThinking0F938ED2] = getString(block[valueThinking0F938ED2]) + getString(delta[valueThinking0F938ED2])
	case "signature_delta":
		block[interactionKeySignature] = getString(block[interactionKeySignature]) + getString(delta[interactionKeySignature])
	case "input_json_delta":
		r.inputs[index] += getString(delta["partial_json"])
	}
	return nil
}

func (r *anthropicReplay) finishInput(index int) error {
	partial := r.inputs[index]
	if partial == "" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(partial))
	decoder.UseNumber()
	var input map[string]interface{}
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("invalid MiniMax tool input in block %d: %w", index, err)
	}
	r.blocks[index][valueInputF8378501] = input
	delete(r.inputs, index)
	return nil
}

func (r *anthropicReplay) encode() string {
	indices := make([]int, 0, len(r.blocks))
	for index := range r.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	blocks := make([]interface{}, 0, len(indices))
	for _, index := range indices {
		blocks = append(blocks, r.blocks[index])
	}
	return encodeProviderReplayItems(r.protocol, blocks)
}
