package model_test

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestPhysicalRequestEvidenceDistinguishesEveryOpaqueField(t *testing.T) {
	raw := json.RawMessage(`{ "x": "<>&", "n":9007199254740993 }`)
	changed := json.RawMessage(`{"x":"<>&","n":9007199254740993}`)
	request := model.Request{RunID: "run", InvocationID: "inv", Model: "fixture", ModelOptions: raw, OutputContract: &model.OutputContract{Name: "answer", Schema: raw},
		Messages: []model.Message{{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: raw}}, ProviderReplay: &model.ProviderReplay{Provider: "fixture", Format: "signed", Data: raw}}},
		Tools:    []tools.Definition{{Key: "lookup", Name: "lookup", InputSchema: raw}}, HostedTools: []model.HostedTool{{Key: "native", Target: raw}}}
	original, err := model.PhysicalRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*model.Request){
		"options":       func(r *model.Request) { r.ModelOptions = changed },
		"schema":        func(r *model.Request) { r.OutputContract.Schema = changed },
		"arguments":     func(r *model.Request) { r.Messages[0].ToolCalls[0].Arguments = changed },
		"replay":        func(r *model.Request) { r.Messages[0].ProviderReplay.Data = changed },
		"local_schema":  func(r *model.Request) { r.Tools[0].InputSchema = changed },
		"hosted_target": func(r *model.Request) { r.HostedTools[0].Target = changed },
	} {
		t.Run(name, func(t *testing.T) {
			r := model.CloneRequest(request)
			mutate(&r)
			got, e := model.PhysicalRequestHash(r)
			if e != nil || got == original {
				t.Fatalf("original evidence collision: %v", e)
			}
		})
	}
	clone, err := model.PhysicalRequestHash(model.CloneRequest(request))
	if err != nil || clone != original {
		t.Fatal("isolated original request changed identity")
	}
}

func TestResponseEvidenceHashPreservesAllOpaqueReceiptFields(t *testing.T) {
	raw := json.RawMessage(`{ "x": "<>&" }`)
	changed := json.RawMessage(`{"x":"<>&"}`)
	response := model.Response{ResponseID: "response", Usage: &model.Usage{InputTokens: 1, OutputTokens: 2}, ToolCalls: []tools.Call{{ID: "call", Arguments: raw}},
		HostedToolCalls: []model.HostedToolCall{{ID: "native", Input: raw, Output: raw, Error: raw}}, Artifacts: []model.ArtifactRef{{ID: "artifact", Metadata: raw}}, ProviderReplay: &model.ProviderReplay{Provider: "fixture", Format: "signed", Data: raw}}
	original, err := model.ResponseEvidenceHash(response)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*model.Response){
		"arguments": func(r *model.Response) { r.ToolCalls[0].Arguments = changed }, "replay": func(r *model.Response) { r.ProviderReplay.Data = changed },
		"hosted_input": func(r *model.Response) { r.HostedToolCalls[0].Input = changed }, "hosted_output": func(r *model.Response) { r.HostedToolCalls[0].Output = changed }, "hosted_error": func(r *model.Response) { r.HostedToolCalls[0].Error = changed }, "artifact": func(r *model.Response) { r.Artifacts[0].Metadata = changed },
	} {
		t.Run(name, func(t *testing.T) {
			r := model.CloneResponse(response)
			mutate(&r)
			got, e := model.ResponseEvidenceHash(r)
			if e != nil || got == original {
				t.Fatalf("original receipt evidence collision: %v", e)
			}
		})
	}
	clone, err := model.ResponseEvidenceHash(model.CloneResponse(response))
	if err != nil || clone != original {
		t.Fatal("isolated original response changed identity")
	}
}
