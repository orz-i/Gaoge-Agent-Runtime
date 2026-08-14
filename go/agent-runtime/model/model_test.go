package model_test

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const testToolKey = "lookup"

func TestCloneRequestIsolatesNestedModelContract(t *testing.T) {
	t.Parallel()
	request := model.Request{
		RunID: "run-1", Model: "model-1", ModelOptions: json.RawMessage(`{"temperature":1}`),
		Messages: []model.Message{{
			Role:      model.RoleAssistant,
			ToolCalls: []tools.Call{{ID: "call-1", ToolKey: testToolKey, Arguments: json.RawMessage(`{"id":1}`)}},
		}},
		Tools:       []tools.Definition{{Key: testToolKey, Name: testToolKey, InputSchema: json.RawMessage(`{"type":"object"}`), ApprovalMode: tools.ApprovalNever}},
		HostedTools: []model.HostedTool{{Key: "web", Target: json.RawMessage(`{"provider":"openai"}`)}},
	}
	cloned := model.CloneRequest(request)
	cloned.ModelOptions[1] = 'X'
	cloned.Messages[0].ToolCalls[0].Arguments[1] = 'X'
	cloned.Tools[0].InputSchema[1] = 'X'
	cloned.HostedTools[0].Target[1] = 'X'
	if string(request.ModelOptions) != `{"temperature":1}` || string(request.Messages[0].ToolCalls[0].Arguments) != `{"id":1}` ||
		string(request.Tools[0].InputSchema) != `{"type":"object"}` || string(request.HostedTools[0].Target) != `{"provider":"openai"}` {
		t.Fatalf("clone mutated source request: %#v", request)
	}
}

func TestCloneResponseIsolatesHostedFacts(t *testing.T) {
	t.Parallel()
	response := model.Response{
		ToolCalls:       []tools.Call{{ID: "call-1", ToolKey: testToolKey, Arguments: json.RawMessage(`{}`)}},
		HostedToolCalls: []model.HostedToolCall{{ToolKey: "web", Output: json.RawMessage(`{"ok":true}`)}},
		Artifacts:       []model.ArtifactRef{{ID: "artifact-1", Kind: "image", Metadata: json.RawMessage(`{"w":1}`)}},
		Citations:       []string{"source-1"},
	}
	cloned := model.CloneResponse(response)
	cloned.ToolCalls[0].Arguments[0] = '['
	cloned.HostedToolCalls[0].Output[0] = '['
	cloned.Artifacts[0].Metadata[0] = '['
	cloned.Citations[0] = "mutated"
	if string(response.ToolCalls[0].Arguments) != `{}` || string(response.HostedToolCalls[0].Output) != `{"ok":true}` ||
		string(response.Artifacts[0].Metadata) != `{"w":1}` || response.Citations[0] != "source-1" {
		t.Fatalf("clone mutated source response: %#v", response)
	}
}
