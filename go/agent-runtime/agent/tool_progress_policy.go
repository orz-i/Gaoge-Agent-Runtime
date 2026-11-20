package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ToolProgressPolicy is an opt-in Host solving policy, not execution authority.
// ExplorationToolKeys must name local, nonterminal read/search tools whose
// arguments can be changed to find new evidence. Do not opt in writes, polling,
// delegation or tools that need approval to acquire additional authority.
// The Host certifies read semantics; a Tool definition alone cannot prove them.
// Nil preserves the historical whole-tool suppression behavior.
type ToolProgressPolicy struct {
	ExplorationToolKeys []string `json:"explorationToolKeys,omitempty"`
}

func normalizeToolProgressPolicy(policy *ToolProgressPolicy, catalog tools.Catalog) (*ToolProgressPolicy, error) {
	if policy == nil || len(policy.ExplorationToolKeys) == 0 {
		return nil, nil
	}
	if jsoncontract.ValidateCanonicalUTF8(policy) != nil {
		return nil, ErrInvalidRequest
	}
	keys := normalizedToolKeys(policy.ExplorationToolKeys)
	if len(keys) != len(policy.ExplorationToolKeys) || catalog == nil {
		return nil, ErrInvalidRequest
	}
	for _, key := range keys {
		definition, ok := catalog.Resolve(key)
		if !ok || definition.Key != key || definition.Terminal || key == ToolDiscoveryKey || key == "harness.delegate_agent" ||
			tools.ValidateDefinition(definition) != nil {
			return nil, ErrInvalidRequest
		}
	}
	return &ToolProgressPolicy{ExplorationToolKeys: keys}, nil
}

func cloneToolProgressPolicy(policy *ToolProgressPolicy) *ToolProgressPolicy {
	if policy == nil {
		return nil
	}
	return &ToolProgressPolicy{ExplorationToolKeys: append([]string(nil), policy.ExplorationToolKeys...)}
}

func freezeToolProgressPolicy(policy *ToolProgressPolicy, selected []string) *ToolProgressPolicy {
	if policy == nil {
		return nil
	}
	var keys []string
	for _, key := range policy.ExplorationToolKeys {
		if slices.Contains(selected, key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return &ToolProgressPolicy{ExplorationToolKeys: keys}
}

func validFrozenToolProgressPolicy(state runState) bool {
	policy := state.ToolProgressPolicy
	if policy == nil {
		return true
	}
	if jsoncontract.ValidateCanonicalUTF8(policy) != nil {
		return false
	}
	keys := policy.ExplorationToolKeys
	if len(keys) == 0 || !slices.Equal(keys, normalizedToolKeys(keys)) {
		return false
	}
	for _, key := range keys {
		if !slices.Contains(state.ToolKeys, key) || key == ToolDiscoveryKey || key == "harness.delegate_agent" {
			return false
		}
	}
	return true
}

func explorationTool(state runState, key string) bool {
	return state.ToolProgressPolicy != nil && slices.Contains(state.ToolProgressPolicy.ExplorationToolKeys, key)
}

// Never relax an explicit unavailable-capability refusal. This deliberately
// retains the existing denial boundary, even for an incorrectly opted-in tool.
func progressBlocks(state runState, keys []string) []string {
	if state.ToolProgressPolicy == nil || len(keys) == 0 {
		return keys
	}
	var blocked []string
	byID := toolCallKeysByID(state.Messages)
	for _, key := range keys {
		keep := !explorationTool(state, key)
		if !keep {
			for _, message := range state.Messages {
				if message.Role == model.RoleTool && byID[message.ToolCallID] == key && unavailableEvidenceToolResult(model.TextValue(message.Content)) {
					keep = true
					break
				}
			}
		}
		if keep {
			blocked = append(blocked, key)
		}
	}
	return blocked
}

func projectToolProgress(state runState, definitions []tools.Definition, hosted []model.HostedTool, messages []model.Message) ([]tools.Definition, []model.HostedTool, []model.Message) {
	repeated := repeatedUnchangedToolKeys(messages)
	blocked := progressBlocks(state, repeated)
	if len(blocked) != 0 {
		definitions = definitionsWithoutKeys(definitions, blocked)
		hosted = hostedToolsWithoutKeys(hosted, blocked)
		messages = withRepeatedToolGuidance(messages, blocked)
	}
	stalled := unproductiveToolKeys(messages)
	stopped := progressBlocks(state, stalled)
	if len(stopped) != 0 {
		definitions = definitionsWithoutKeys(definitions, stopped)
		hosted = hostedToolsWithoutKeys(hosted, stopped)
		messages = withSystemGuidance(messages,
			"Repeated attempts to use these tools returned no authorized match or recoverable failures: "+strings.Join(stopped, ", ")+
				". Do not repeat the same operation using altered parameters. Continue with available evidence or finish now, clearly reporting any missing current information or sources. Never invent citations.")
	}
	if len(blocked) != len(repeated) || len(stopped) != len(stalled) {
		messages = withSystemGuidance(messages, "Authorized read/search tools remain available for a different query or source. Repeated failures are not evidence. Change the arguments to seek new evidence; identical settled calls are bounded. Existing call and budget limits still apply. Finish honestly if no evidence can be obtained.")
	}
	return definitions, hosted, messages
}

// This is a new-call guard, never a replay decision. Callers must check the
// pending physical dispatch marker and recovered receipts before applying it.
func repeatedExplorationCall(state runState, call tools.Call) bool {
	if !explorationTool(state, call.ToolKey) {
		return false
	}
	arguments := progressArguments(call.Arguments)
	count := 0
	calls := make(map[string]tools.Call)
	settled := make(map[string]bool)
	for _, message := range state.Messages {
		if message.Role == model.RoleAssistant {
			for _, previous := range message.ToolCalls {
				calls[previous.ID] = previous
			}
		}
		previous, ok := calls[message.ToolCallID]
		if message.Role == model.RoleTool && ok && !settled[message.ToolCallID] {
			settled[message.ToolCallID] = true
			if previous.ToolKey != call.ToolKey || progressArguments(previous.Arguments) != arguments {
				continue
			}
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

func progressArguments(arguments json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return string(arguments) // normal Tool schema validation owns invalid input
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return string(arguments)
	}
	return string(canonical)
}
