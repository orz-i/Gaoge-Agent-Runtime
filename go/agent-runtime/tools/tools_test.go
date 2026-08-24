package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const largeToolKey = "large"

func TestValidateExecutionResultEnforcesHardContentLimit(t *testing.T) {
	t.Parallel()
	valid := tools.ExecutionResult{
		Content: json.RawMessage(`{"ok":true}`),
		Receipt: tools.Receipt{ExecutionID: "execution-1", Disposition: "read"},
	}
	if err := tools.ValidateExecutionResult(valid); err != nil {
		t.Fatalf("valid Tool result rejected: %v", err)
	}
	oversized := valid
	oversized.Content = json.RawMessage(`{"payload":"` + strings.Repeat("x", tools.MaxExecutionResultContentBytes) + `"}`)
	if err := tools.ValidateExecutionResult(oversized); !errors.Is(err, tools.ErrInvalidCall) {
		t.Fatalf("oversized Tool result must fail closed, got %v", err)
	}
}

func TestRegistryRejectsOversizedExecutionResult(t *testing.T) {
	t.Parallel()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: largeToolKey, Name: largeToolKey, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"payload":"` + strings.Repeat("x", tools.MaxExecutionResultContentBytes) + `"}`),
				Receipt: tools.Receipt{ExecutionID: "execution-large", Disposition: "read"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run-large", Call: tools.Call{ID: "call-large", ToolKey: largeToolKey, Arguments: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, tools.ErrInvalidCall) {
		t.Fatalf("Registry accepted oversized Tool result: %v", err)
	}
}
