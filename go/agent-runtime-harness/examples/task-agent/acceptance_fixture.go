package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type acceptanceFixtureModel struct{ task string }

func (client acceptanceFixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	completed := 0
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			completed++
		}
	}
	type action struct {
		key   string
		input any
	}
	var actions []action
	switch client.task {
	case "code":
		actions = []action{{"read_code", map[string]any{}}, {"read_tests", map[string]any{}}, {"run_tests", map[string]any{}},
			{"write_code", map[string]string{"source": acceptanceFixedCode}}, {"run_tests", map[string]any{}}}
	case "documents":
		actions = []action{{"list_documents", map[string]any{}}, {"read_document", map[string]string{"id": "D-01"}},
			{"read_document", map[string]string{"id": "D-02"}},
			{"save_answer", map[string]any{"quantity": 42, "delivery": "2099-08-07", "source": "D-02:P2"}}}
	case "approval":
		actions = []action{{"write_approved", map[string]any{}}}
	}
	if completed >= len(actions) {
		return model.Response{Content: "Task actions completed; validate the actual artifact independently."}, nil
	}
	selected := actions[completed]
	arguments, _ := json.Marshal(selected.input)
	return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("task-action-%d", completed+1), ToolKey: selected.key, Arguments: arguments}}}, nil
}
