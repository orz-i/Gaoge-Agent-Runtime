package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const acceptanceDocuments = `{"D-01":"Superseded draft: quantity=30; delivery=2099-08-01.","D-02":"Approved amendment. Supersedes D-01. Final quantity=42; delivery=2099-08-07. Evidence paragraph D-02:P2."}`
const acceptanceDocumentsDistractors = `{"D-01":"Superseded draft: quantity=30; delivery=2099-08-01.","D-02":"Approved amendment. Supersedes D-01. Final quantity=42; delivery=2099-08-07. Evidence paragraph D-02:P2.","D-03":"Quantity=42; delivery=2099-08-05. This is an unsigned vendor note, not an approved document.","D-04":"Supplemental access note: warehouse gate opens 07:00. It does not modify quantity or delivery."}`

func acceptanceDocumentsMap(source string) (map[string]string, error) {
	var result map[string]string
	if err := json.Unmarshal([]byte(source), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (task *acceptanceTask) prepare() ([]tools.Registration, error) {
	switch task.name {
	case "code", "code-negative", "code-degenerate":
		return task.prepareCode()
	case "documents", "documents-distractors":
		task.expectedQuantity, task.expectedDelivery, task.expectedSource = 42, "2099-08-07", "D-02:P2"
		task.goal = "Read the synthetic draft and approved amendment. Determine the final approved quantity and delivery date; save the answer with its original paragraph reference. Do not use superseded draft facts."
		documentSource := acceptanceDocuments
		documentIDs := []string{"D-01", "D-02"}
		if task.name == "documents-distractors" {
			documentSource = acceptanceDocumentsDistractors
			documentIDs = []string{"D-04", "D-01", "D-03", "D-02"}
			task.goal = "Read all four listed records, distinguish the unsigned vendor note and access note from the approved amendment, and save the final approved quantity, delivery date and original paragraph reference."
		}
		var err error
		task.documents, err = acceptanceDocumentsMap(documentSource)
		if err != nil {
			return nil, err
		}
		readSchema, err := json.Marshal(map[string]any{
			"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "enum": documentIDs}},
			"required": []string{"id"}, "additionalProperties": false,
		})
		if err != nil {
			return nil, err
		}
		return []tools.Registration{
			acceptanceRegistration("list_documents", "List fixture document IDs", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return acceptanceReceipt(request, documentIDs)
			}),
			acceptanceRegistration("read_document", "Read one original fixture document", string(readSchema), func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				var input struct{ ID string }
				if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
					return tools.ExecutionResult{}, err
				}
				original, ok := task.documents[input.ID]
				if !ok {
					return acceptanceRefusal(request, "documents.unknown_id", "Read only an ID returned by list_documents")
				}
				task.readDocuments[input.ID] = true
				return acceptanceReceipt(request, map[string]string{"id": input.ID, "original": original})
			}),
			// Preserve schema validity but let wrong business facts reach the
			// artifact. The independent Oracle, not an input const, must catch it.
			acceptanceRegistration("save_answer", "Save the final document answer as an actual artifact", `{"type":"object","properties":{"quantity":{"type":"integer"},"delivery":{"type":"string"},"source":{"type":"string"}},"required":["quantity","delivery","source"],"additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if err := os.WriteFile(task.artifactPath("answer.json"), request.Call.Arguments, 0600); err != nil {
					return tools.ExecutionResult{}, err
				}
				return acceptanceReceipt(request, map[string]bool{"saved": true})
			}),
		}, nil
	case "approval", "approval-denied":
		task.goal = "Request approval to create the fixed local record. Do not claim it was written before an operator decision."
		if task.name == "approval-denied" {
			task.goal += ` If rejected, do not retry or claim it was written. Finish with JSON {"status":"denied","written":false}.`
		}
		return []tools.Registration{acceptanceRegistration("write_approved", "Create a fixed scratch record after native approval; no money, network, or business effect", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			task.approvedWrites++ // deliberately not deduplicated: retries are visible
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
	case "code", "code-negative", "code-degenerate":
		return task.verifyCode(ctx, checks)
	case "documents", "documents-distractors":
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
		checks["approved_amendment_read"] = task.readDocuments["D-02"]
		checks["superseded_draft_compared"] = task.readDocuments["D-01"]
		if task.name == "documents-distractors" {
			checks["distractors_reviewed"] = task.readDocuments["D-03"] && task.readDocuments["D-04"]
		}
		checks["final_facts_and_original_reference"] = json.Unmarshal(content, &answer) == nil && answer.Quantity == task.expectedQuantity && answer.Delivery == task.expectedDelivery && answer.Source == task.expectedSource
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
	case "approval-denied":
		content, err := os.ReadFile(task.artifactPath("approved.txt"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		checks["denial_created_no_artifact"] = errors.Is(err, os.ErrNotExist) && task.approvedWrites == 0
		var text string
		var answer struct {
			Status  string `json:"status"`
			Written *bool  `json:"written"`
		}
		checks["answer_reports_denial"] = json.Unmarshal([]byte(task.answer), &text) == nil &&
			json.Unmarshal([]byte(text), &answer) == nil && answer.Status == "denied" && answer.Written != nil && !*answer.Written
		return content, nil
	default:
		return nil, errors.New("unknown acceptance oracle")
	}
}
