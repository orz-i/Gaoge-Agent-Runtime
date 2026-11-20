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
	if codeCase, code := acceptanceCodeCase(client.task); code {
		actions = []action{{"read_code", map[string]any{}}, {"read_tests", map[string]any{}}, {"run_tests", map[string]any{}},
			{"write_code", map[string]string{"source": codeCase.expected}}, {"run_tests", map[string]any{}}}
	} else if client.task == "documents" || client.task == "documents-distractors" {
		documents := []string{"D-01", "D-02"}
		if client.task == "documents-distractors" {
			documents = []string{"D-04", "D-01", "D-03", "D-02"}
		}
		actions = append(actions, action{"list_documents", map[string]any{}})
		for _, id := range documents {
			actions = append(actions, action{"read_document", map[string]string{"id": id}})
		}
		actions = append(actions, action{"save_answer", map[string]any{"quantity": 42, "delivery": "2099-08-07", "source": "D-02:P2"}})
	} else if client.task == "approval" || client.task == "approval-denied" {
		actions = []action{{"write_approved", map[string]any{}}}
	}
	if client.task == "approval-denied" && completed >= len(actions) {
		return model.Response{Content: `{"status":"denied","written":false}`}, nil
	}
	if completed >= len(actions) {
		return model.Response{Content: "Task actions completed; validate the actual artifact independently."}, nil
	}
	selected := actions[completed]
	arguments, _ := json.Marshal(selected.input)
	return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("task-action-%d", completed+1), ToolKey: selected.key, Arguments: arguments}}}, nil
}
