package agentruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestModelCapsFromCapabilitiesOverridesNameInference(t *testing.T) {
	caps := GetModelCapsFromCapabilities("custom-enterprise-model", `{
		"contextWindow": 64000,
		"maxOutputTokens": 12000
	}`)

	if caps.ContextWindow != 64_000 {
		t.Fatalf("expected context window from capabilities, got %d", caps.ContextWindow)
	}
	if caps.MaxOutputTokens != 12_000 {
		t.Fatalf("expected max output from capabilities, got %d", caps.MaxOutputTokens)
	}
}

func TestEffectiveContextBudgetFromCapabilitiesUsesConfiguredWindow(t *testing.T) {
	got := EffectiveContextBudgetFromCapabilities("custom-enterprise-model", `{
		"context_window_tokens": "64000",
		"max_output_tokens": "12000"
	}`)
	want := 64_000 - 12_000 - autocompactBufferTokens
	if got != want {
		t.Fatalf("expected budget %d, got %d", want, got)
	}
}

type routeValidationWorkspace struct {
	got WorkspaceRouteValidation
}

func (*routeValidationWorkspace) CompileWorkspace(context.Context, domain.ActorRef, domain.ThreadRef, *WorkspaceRequest, int) (*WorkspaceSnapshot, error) {
	return &WorkspaceSnapshot{}, nil
}

func (*routeValidationWorkspace) ExecuteWorkspaceTool(context.Context, WorkspaceToolExecution) (string, error) {
	return "", nil
}

func (w *routeValidationWorkspace) ValidateWorkspaceRoute(_ context.Context, input WorkspaceRouteValidation) error {
	w.got = input
	return nil
}

func TestValidateWorkspaceRouteUsesProviderWireMeasurement(t *testing.T) {
	provider := &routeValidationWorkspace{}
	service := &Engine{workspaces: NewWorkspaceRegistry(map[string]WorkspaceProvider{"document": provider})}
	tools := []ToolDefinition{{Name: "document_read", Description: valueRead3A612695, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	effective := effectiveTextRunConfig{Workspace: &WorkspaceSnapshot{Request: ResolvedWorkspaceContext{Type: "document"}}}
	if err := service.validateWorkspaceRoute(t.Context(), effective, tools, `{"custom":true}`, protocolGoogleGenerateContent); err != nil {
		t.Fatalf("validate route: %v", err)
	}
	if provider.got.ToolCount != 1 || provider.got.ProviderToolPayloadBytes <= 0 || !provider.got.ProviderPayloadObserved {
		t.Fatalf("validation input = %#v", provider.got)
	}
	if provider.got.ModelCapabilitiesJSON != `{"custom":true}` || provider.got.ProviderProtocol != protocolGoogleGenerateContent {
		t.Fatalf("route identity = %#v", provider.got)
	}
}

func TestValidateWorkspaceRouteSkipsRunWithoutWorkspace(t *testing.T) {
	service := &Engine{}
	if err := service.validateWorkspaceRoute(t.Context(), effectiveTextRunConfig{}, []ToolDefinition{{Name: "x"}}, `{}`, protocolOpenAIResponses); err != nil {
		t.Fatalf("non-workspace route: %v", err)
	}
}
