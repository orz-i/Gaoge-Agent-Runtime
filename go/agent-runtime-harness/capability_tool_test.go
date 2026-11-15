package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type capabilityToolModel struct {
	t                *testing.T
	calls            int
	correctArguments bool
}

func (model *capabilityToolModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	invocationCall := 1
	arguments := json.RawMessage(`{"commandID":"workflow","goal":"child workflow","arguments":{}}`)
	if model.correctArguments {
		if model.calls == 1 {
			return runtimemodel.Response{ToolCalls: []tools.Call{{ID: "invalid-revision", ToolKey: harness.CapabilityInvocationToolKey, Arguments: json.RawMessage(`{"commandID":"workflow","goal":"child workflow","arguments":{"definitionReference":{"id":"fixture-workflow","revision":"1"}}}`)}}}, nil
		}
		invocationCall = 2
		arguments = json.RawMessage(`{"commandID":"workflow","goal":"child workflow","arguments":{"definitionReference":{"id":"fixture-workflow","revision":1}}}`)
		if model.calls == invocationCall {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != runtimemodel.RoleTool || last.ToolCallID != "invalid-revision" || !strings.Contains(runtimemodel.TextValue(last.Content), "/definitionReference/revision") || !strings.Contains(runtimemodel.TextValue(last.Content), "integer") {
				model.t.Fatalf("missing actionable type correction: %#v", last)
			}
		}
	}
	if model.calls == invocationCall {
		if len(request.Tools) != 1 || request.Tools[0].Key != harness.CapabilityInvocationToolKey {
			model.t.Fatalf("root capability Tools = %#v", request.Tools)
		}
		schema := string(request.Tools[0].InputSchema)
		if !strings.Contains(schema, `"const":"workflow"`) || strings.Contains(schema, `"definition"`) || strings.Contains(schema, `"members"`) {
			model.t.Fatalf("capability schema leaked Runtime topology: %s", schema)
		}
		return runtimemodel.Response{ToolCalls: []tools.Call{{ID: "invoke-workflow", ToolKey: harness.CapabilityInvocationToolKey, Arguments: arguments}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != runtimemodel.RoleTool || last.ToolCallID != "invoke-workflow" || !strings.Contains(runtimemodel.TextValue(last.Content), "child complete") {
		model.t.Fatalf("resumed capability transcript = %#v", last)
	}
	return runtimemodel.Response{Content: "root complete"}, nil
}

var _ runtimemodel.Client = (*capabilityToolModel)(nil)
