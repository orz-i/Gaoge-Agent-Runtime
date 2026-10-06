package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errToolHandler = errors.New("tool handler failed")

func TestRegistryCatalogAndExecutionBoundaries(t *testing.T) {
	t.Parallel()
	sharedContent := json.RawMessage(`{"ok":true}`)
	registry, err := tools.NewRegistry([]tools.Registration{
		{
			Definition: tools.Definition{
				Key: " alpha ", Name: " alpha ", Description: " first ",
				InputSchema: json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if request.RunID != "run" || request.Call.ID != "call" {
					t.Fatalf("request=%#v", request)
				}
				return tools.ExecutionResult{
					Content: sharedContent,
					Receipt: tools.Receipt{ExecutionID: "execution", Disposition: "committed"},
				}, nil
			}),
		},
		{
			Definition: tools.Definition{
				Key: "beta", Name: "beta", InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"beta":true}`),
					Receipt: tools.Receipt{ExecutionID: "beta", Disposition: "read"},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := registry.Descriptor()
	if descriptor.Name != "tools" || len(descriptor.Provides) != 2 ||
		descriptor.Provides[0] != tools.CapabilityCatalog || descriptor.Provides[1] != tools.CapabilityExecutor {
		t.Fatalf("descriptor=%#v", descriptor)
	}

	definition, ok := registry.Resolve(" alpha ")
	if !ok || definition.Key != "alpha" || definition.Name != "alpha" || definition.Description != "first" {
		t.Fatalf("definition=%#v ok=%t", definition, ok)
	}
	definition.InputSchema[0] = 'x'
	resolvedAgain, ok := registry.Resolve("alpha")
	if !ok || !json.Valid(resolvedAgain.InputSchema) {
		t.Fatalf("registry definition was mutated: %#v", resolvedAgain)
	}
	if _, ok = registry.Resolve("missing"); ok {
		t.Fatal("missing definition resolved")
	}
	var unavailable *tools.Registry
	if _, ok = unavailable.Resolve("alpha"); ok {
		t.Fatal("nil registry resolved definition")
	}

	listed, err := registry.List([]string{"beta", " alpha ", "beta"})
	if err != nil || len(listed) != 2 || listed[0].Key != "beta" || listed[1].Key != "alpha" {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	if _, err = registry.List([]string{"missing"}); !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("missing list error=%v", err)
	}
	if _, err = registry.List([]string{" "}); !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("empty list error=%v", err)
	}

	result, err := registry.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run",
		Call:  tools.Call{ID: "call", ToolKey: "alpha", Arguments: json.RawMessage(`{"value":"ok"}`)},
	})
	if err != nil || string(result.Content) != `{"ok":true}` {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	sharedContent[1] = 'x'
	if string(result.Content) != `{"ok":true}` {
		t.Fatalf("execution result was not isolated: %s", result.Content)
	}
}

func TestRegistryRejectsInvalidRegistrationsAndExecutionEdges(t *testing.T) {
	t.Parallel()
	validDefinition := tools.Definition{Key: "tool", Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
	handler := tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
		return tools.ExecutionResult{
			Content: json.RawMessage(`{"ok":true}`),
			Receipt: tools.Receipt{ExecutionID: "execution", Disposition: "read"},
		}, nil
	})
	if _, err := tools.NewRegistry([]tools.Registration{{Definition: validDefinition}}); !errors.Is(err, tools.ErrInvalidDefinition) {
		t.Fatalf("nil handler error=%v", err)
	}
	if _, err := tools.NewRegistry([]tools.Registration{
		{Definition: validDefinition, Handler: handler},
		{Definition: validDefinition, Handler: handler},
	}); !errors.Is(err, tools.ErrDuplicateTool) {
		t.Fatalf("duplicate error=%v", err)
	}

	registry, err := tools.NewRegistry([]tools.Registration{{Definition: validDefinition, Handler: handler}})
	if err != nil {
		t.Fatal(err)
	}
	invalidRequests := []tools.ExecutionRequest{
		{},
		{RunID: " ", Call: tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`{}`)}},
		{RunID: "run", Call: tools.Call{ToolKey: "tool", Arguments: json.RawMessage(`{}`)}},
		{RunID: "run", Call: tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`not-json`)}},
	}
	for _, request := range invalidRequests {
		if _, err = registry.Execute(t.Context(), request); !errors.Is(err, tools.ErrInvalidCall) {
			t.Fatalf("request=%#v error=%v", request, err)
		}
	}
	var unavailable *tools.Registry
	if _, err = unavailable.Execute(t.Context(), tools.ExecutionRequest{}); !errors.Is(err, tools.ErrInvalidCall) {
		t.Fatalf("nil registry error=%v", err)
	}
	if _, err = registry.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run", Call: tools.Call{ID: "call", ToolKey: "missing", Arguments: json.RawMessage(`{}`)},
	}); !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("missing tool error=%v", err)
	}

	failing, err := tools.NewRegistry([]tools.Registration{{
		Definition: validDefinition,
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{}, errToolHandler
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = failing.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run", Call: tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`{}`)},
	}); !errors.Is(err, errToolHandler) {
		t.Fatalf("handler error=%v", err)
	}

	invalidResult, err := tools.NewRegistry([]tools.Registration{{
		Definition: validDefinition,
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`)}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invalidResult.Execute(t.Context(), tools.ExecutionRequest{
		RunID: "run", Call: tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`{}`)},
	}); !errors.Is(err, tools.ErrInvalidCall) {
		t.Fatalf("invalid result error=%v", err)
	}
}

func TestToolCloneHelpersIsolateJSON(t *testing.T) {
	t.Parallel()
	definition := tools.Definition{Key: "tool", Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
	call := tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`{"value":1}`)}
	request := tools.ExecutionRequest{RunID: "run", Call: call}
	result := tools.ExecutionResult{
		Content: json.RawMessage(`{"ok":true}`),
		Receipt: tools.Receipt{ExecutionID: "execution", Disposition: "read"},
	}
	clonedDefinition := tools.CloneDefinition(definition)
	clonedCall := tools.CloneCall(call)
	clonedRequest := tools.CloneExecutionRequest(request)
	clonedResult := tools.CloneExecutionResult(result)

	definition.InputSchema[1] = 'x'
	call.Arguments[1] = 'x'
	request.Call.Arguments[1] = 'y'
	result.Content[1] = 'x'
	if !json.Valid(clonedDefinition.InputSchema) || !json.Valid(clonedCall.Arguments) ||
		!json.Valid(clonedRequest.Call.Arguments) || !json.Valid(clonedResult.Content) {
		t.Fatal("clone helper retained caller-owned JSON")
	}
}

func TestRecoverableCallErrorMetadataAndBlockedTools(t *testing.T) {
	t.Parallel()
	recoverable := tools.NewRecoverableCallErrorWithBlockedTools(
		" blocked ", " message ", errToolHandler, " beta ", "", "alpha", "beta",
	)
	code, message, ok := tools.RecoverableCallErrorInfo(recoverable)
	if !ok || code != "blocked" || message != "message" || !errors.Is(recoverable, errToolHandler) {
		t.Fatalf("recoverable=%v code=%q message=%q ok=%t", recoverable, code, message, ok)
	}
	blocked := tools.RecoverableCallErrorBlockedToolKeys(recoverable)
	if len(blocked) != 2 || blocked[0] != "beta" || blocked[1] != "alpha" {
		t.Fatalf("blocked=%v", blocked)
	}
	blocked[0] = "mutated"
	again := tools.RecoverableCallErrorBlockedToolKeys(recoverable)
	if again[0] != "beta" {
		t.Fatalf("blocked tool keys were not isolated: %v", again)
	}
	if keys := tools.RecoverableCallErrorBlockedToolKeys(errToolHandler); keys != nil {
		t.Fatalf("ordinary error blocked keys=%v", keys)
	}
	if code, message, ok = tools.RecoverableCallErrorInfo(errToolHandler); ok || code != "" || message != "" {
		t.Fatalf("ordinary error metadata code=%q message=%q ok=%t", code, message, ok)
	}

	long := tools.NewRecoverableCallError("", strings.Repeat("界", 2_100), nil)
	_, message, ok = tools.RecoverableCallErrorInfo(long)
	if !ok || len([]rune(message)) != 2_000 {
		t.Fatalf("truncated message runes=%d ok=%t", len([]rune(message)), ok)
	}
	fallback := tools.NewRecoverableCallError("", "", nil)
	code, message, ok = tools.RecoverableCallErrorInfo(fallback)
	if !ok || code != "tool_call_invalid" || message != tools.ErrInvalidCall.Error() {
		t.Fatalf("fallback code=%q message=%q ok=%t", code, message, ok)
	}

	var nilRecoverable *tools.RecoverableCallError
	if nilRecoverable.Error() != tools.ErrInvalidCall.Error() || nilRecoverable.Unwrap() != nil {
		t.Fatal("nil recoverable error contract changed")
	}
}

func TestValidationRejectsDefinitionAndCallEdges(t *testing.T) {
	t.Parallel()
	if err := tools.ValidateDefinition(tools.Definition{}); !errors.Is(err, tools.ErrInvalidDefinition) {
		t.Fatalf("empty definition error=%v", err)
	}
	definition := tools.Definition{
		Key: "tool", Name: "tool",
		InputSchema: json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"integer"}}}`),
	}
	if err := tools.ValidateCall(definition, tools.Call{
		ID: "call", ToolKey: "other", Arguments: json.RawMessage(`{"value":1}`),
	}); !errors.Is(err, tools.ErrInvalidCall) {
		t.Fatalf("mismatched key error=%v", err)
	}
	if err := tools.ValidateCall(tools.Definition{
		Key: "tool", Name: "tool", InputSchema: json.RawMessage(`{"type":"invalid"}`),
	}, tools.Call{ID: "call", ToolKey: "tool", Arguments: json.RawMessage(`{}`)}); !errors.Is(err, tools.ErrInvalidDefinition) {
		t.Fatalf("invalid schema error=%v", err)
	}
}
