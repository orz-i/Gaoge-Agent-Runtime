package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const acceptanceDocuments = `{"D-01":"Superseded draft: quantity=30; delivery=2099-08-01.","D-02":"Approved amendment. Supersedes D-01. Final quantity=42; delivery=2099-08-07. Evidence paragraph D-02:P2."}`

func (task *acceptanceTask) prepare() ([]tools.Registration, error) {
	switch task.name {
	case "code":
		return task.prepareCode()
	case "documents":
		task.goal = "Read the synthetic draft and amendment, determine the final approved quantity and delivery date, and save the answer with its original paragraph reference. Do not use superseded draft facts."
		return []tools.Registration{
			acceptanceRegistration("list_documents", "List fixture document IDs", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return acceptanceReceipt(request, []string{"D-01", "D-02"})
			}),
			acceptanceRegistration("read_document", "Read one original fixture document", `{"type":"object","properties":{"id":{"type":"string","enum":["D-01","D-02"]}},"required":["id"],"additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				var input struct{ ID string }
				if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
					return tools.ExecutionResult{}, err
				}
				var documents map[string]string
				if err := json.Unmarshal([]byte(acceptanceDocuments), &documents); err != nil {
					return tools.ExecutionResult{}, err
				}
				task.readDocuments[input.ID] = true
				return acceptanceReceipt(request, map[string]string{"id": input.ID, "original": documents[input.ID]})
			}),
			acceptanceRegistration("save_answer", "Save the final document answer as an actual artifact", `{"type":"object","properties":{"quantity":{"type":"integer"},"delivery":{"type":"string"},"source":{"type":"string"}},"required":["quantity","delivery","source"],"additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if err := os.WriteFile(task.artifactPath("answer.json"), request.Call.Arguments, 0600); err != nil {
					return tools.ExecutionResult{}, err
				}
				return acceptanceReceipt(request, map[string]bool{"saved": true})
			}),
		}, nil
	case "approval":
		task.goal = "Create the fixed local approval record using write_approved. It requires an operator decision. Do not claim it was written before approval."
		return []tools.Registration{acceptanceRegistration("write_approved", "Create a fixed scratch record after native approval; no money, network, or business effect", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			task.approvedWrites++ // intentionally no deduplication to expose replay
			if err := os.WriteFile(task.artifactPath("approved.txt"), []byte("approved fixture record\n"), 0600); err != nil {
				return tools.ExecutionResult{}, err
			}
			return acceptanceReceipt(request, map[string]bool{"written": true})
		})}, nil
	default:
		return nil, errors.New("unknown acceptance task")
	}
}

func (task *acceptanceTask) verify(ctx context.Context, checks map[string]bool) ([]byte, error) {
	switch task.name {
	case "code":
		return task.verifyCode(ctx, checks)
	case "documents":
		content, err := os.ReadFile(task.artifactPath("answer.json"))
		if errors.Is(err, os.ErrNotExist) {
			checks["answer_artifact_exists"] = false
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var answer struct {
			Quantity int
			Delivery string
			Source   string
		}
		checks["answer_artifact_exists"] = true
		checks["original_amendment_read"] = task.readDocuments["D-02"]
		checks["both_original_documents_read"] = task.readDocuments["D-01"] && task.readDocuments["D-02"]
		checks["final_facts_and_original_reference"] = json.Unmarshal(content, &answer) == nil && answer.Quantity == 42 && answer.Delivery == "2099-08-07" && answer.Source == "D-02:P2"
		return content, nil
	case "approval":
		content, err := os.ReadFile(task.artifactPath("approved.txt"))
		if errors.Is(err, os.ErrNotExist) {
			checks["approved_artifact_exists"] = false
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		checks["approved_artifact_exists"] = string(content) == "approved fixture record\n"
		checks["approved_write_once"] = task.approvedWrites == 1
		return content, nil
	default:
		return nil, errors.New("unknown acceptance oracle")
	}
}
