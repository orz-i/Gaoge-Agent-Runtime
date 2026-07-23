package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueLocalDispatchF8689687 = "local_dispatch"
	valueSystem499D51AB        = "system"
)

type selectedToolRuntime struct {
	definitions []ToolDefinition
}

func injectMCPToolGuidance(messages []Message, runtime selectedToolRuntime, customPrompt string) []Message {
	if len(runtime.definitions) == 0 {
		return messages
	}
	content := strings.TrimSpace(customPrompt)
	if content == "" {
		content = defaultMCPToolGuidancePrompt()
	}
	insertAt := 0
	for insertAt < len(messages) && messages[insertAt].Role == valueSystem499D51AB {
		insertAt++
	}
	next := make([]Message, 0, len(messages)+1)
	next = append(next, messages[:insertAt]...)
	next = append(next, Message{Role: valueSystem499D51AB, Content: content})
	next = append(next, messages[insertAt:]...)
	return next
}

func defaultMCPToolGuidancePrompt() string {
	return strings.TrimSpace("# tool_use\n- Tools are declared separately via the API schema; follow that schema exactly.\n- Use tools only for external, realtime, private, or explicitly requested data.\n- Use the fewest useful calls; each call must add new information.\n- Do not repeat an identical failed call. Adjust arguments, use another tool, or answer from available evidence.\n- If tools fail or lack enough data, state the gap in the final answer.\n- Do not expose raw tool JSON, internal fields, or tool logs unless the user asks.")
}

func (s *Engine) resolveSelectedToolRuntime(ctx context.Context, actor domain.ActorRef, toolKeys []string) selectedToolRuntime {
	if s.toolCatalog == nil || len(toolKeys) == 0 {
		return selectedToolRuntime{}
	}
	tools, _, err := s.toolCatalog.ResolveAvailable(ctx, actor, uniqueRuntimeStrings(toolKeys), "", "", "")
	if err != nil || len(tools) == 0 {
		return selectedToolRuntime{}
	}
	result := selectedToolRuntime{definitions: make([]ToolDefinition, 0, len(tools))}
	usedNames := map[string]int{}
	for _, tool := range tools {
		if tool.ExecutionMode != valueLocalDispatchF8689687 {
			continue
		}
		name := uniqueModelToolName(NormalizeToolName(tool.ModelName), usedNames)
		if name == "" || strings.TrimSpace(tool.ProviderKey) == "" {
			continue
		}
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage("{\"type\":\"object\",\"properties\":{}}")
		}
		result.definitions = append(result.definitions, ToolDefinition{Name: name, Description: strings.TrimSpace(tool.Description), InputSchema: schema})
	}
	return result
}

func uniqueModelToolName(base string, used map[string]int) string {
	value := strings.TrimSpace(base)
	if value == "" {
		return ""
	}
	count := used[value]
	used[value] = count + 1
	if count == 0 {
		return value
	}
	suffix := fmt.Sprintf("_%d", count+1)
	if len(value)+len(suffix) > 64 {
		value = value[:64-len(suffix)]
	}
	return value + suffix
}
