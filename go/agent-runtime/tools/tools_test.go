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

func TestRegistryRejectsInvalidSchemaAtRegistration(t *testing.T) {
	t.Parallel()
	_, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: "invalid-schema", Name: "invalid_schema",
			InputSchema: json.RawMessage(`{"type":"not-a-real-type"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{}, nil
		}),
	}})
	if !errors.Is(err, tools.ErrInvalidDefinition) {
		t.Fatalf("invalid schema registration = %v", err)
	}
}

func TestRegistryRejectsSchemaViolationBeforeHandler(t *testing.T) {
	t.Parallel()
	executions := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: "publish", Name: "publish",
			InputSchema: json.RawMessage(`{
				"type":"object","additionalProperties":false,"required":["title"],
				"properties":{"title":{"type":"string","minLength":1}}
			}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "execution", Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run", Call: tools.Call{
			ID: "call", ToolKey: "publish", Arguments: json.RawMessage(`{"unexpected":true}`),
		},
	})
	code, message, recoverable := tools.RecoverableCallErrorInfo(err)
	if !recoverable || code != "tool.arguments_schema" || !strings.Contains(message, "schema") || executions != 0 {
		t.Fatalf("schema error = %v code=%q message=%q executions=%d", err, code, message, executions)
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
