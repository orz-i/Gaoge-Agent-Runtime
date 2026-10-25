package openai

import (
	"bytes"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/internal/providerhttp"
)

// completedReasoningItems collects only fully completed reasoning items from
// response.output_item.done. Never use output_item.added: its encrypted_content
// can still be partial. Each item is bound to its absolute output index and ID.
type completedReasoningItems struct {
	byIndex map[int]json.RawMessage
	size    int
}

func (items *completedReasoningItems) observe(index *int, raw json.RawMessage) error {
	var identity struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &identity) != nil {
		return providerhttp.ErrProtocol
	}
	if identity.Type != "reasoning" {
		return nil
	}
	if index == nil || *index < 0 || *index >= maxReplayItems ||
		identity.ID == "" || items.size+len(raw) > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	if items.byIndex == nil {
		items.byIndex = make(map[int]json.RawMessage)
	}
	if _, exists := items.byIndex[*index]; exists {
		return providerhttp.ErrProtocol
	}
	items.byIndex[*index] = append(json.RawMessage(nil), raw...)
	items.size += len(raw)
	return nil
}

// reconcile uses the completed SSE item only to supply encrypted reasoning that
// the final response omitted. Other fields, identity and output positions must
// agree, except that the final response may omit an optional done status
// of "completed". Mismatches still fail closed; in particular no encrypted
// reasoning is fabricated from summaries.
func (items *completedReasoningItems) reconcile(final *responseBody) error {
	for index, raw := range items.byIndex {
		if index >= len(final.Output) {
			return providerhttp.ErrProtocol
		}
		var done, complete map[string]json.RawMessage
		if json.Unmarshal(raw, &done) != nil ||
			json.Unmarshal(final.Output[index], &complete) != nil ||
			done == nil || complete == nil {
			return providerhttp.ErrProtocol
		}
		var doneIdentity, finalIdentity struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if json.Unmarshal(raw, &doneIdentity) != nil ||
			json.Unmarshal(final.Output[index], &finalIdentity) != nil ||
			doneIdentity.Type != "reasoning" || finalIdentity.Type != "reasoning" ||
			doneIdentity.ID == "" || doneIdentity.ID != finalIdentity.ID {
			return providerhttp.ErrProtocol
		}
		originalEncryption := done["encrypted_content"]
		delete(done, "encrypted_content")
		delete(complete, "encrypted_content")
		// Reasoning status is optional in Responses. A completed SSE item may
		// carry status: "completed" while response.completed omits the status.
		// Accept only that one omission; incomplete/in-progress/null status,
		// mismatched present values and all other field differences still fail.
		if rawStatus, present := done["status"]; present {
			var status string
			if json.Unmarshal(rawStatus, &status) != nil || status != "completed" {
				return providerhttp.ErrProtocol
			}
			if _, present := complete["status"]; !present {
				delete(done, "status")
			}
		}
		doneBase, err := json.Marshal(done)
		if err != nil {
			return providerhttp.ErrProtocol
		}
		completeBase, err := json.Marshal(complete)
		if err != nil || !bytes.Equal(doneBase, completeBase) {
			return providerhttp.ErrProtocol
		}
		if doneIdentity.EncryptedContent == "" {
			// The final response may still provide usable evidence.
			continue
		}
		if finalIdentity.EncryptedContent != "" {
			if finalIdentity.EncryptedContent != doneIdentity.EncryptedContent {
				return providerhttp.ErrProtocol
			}
			continue
		}
		// Keep the final response's other fields byte-for-byte in meaning.
		// The outbound provider replay is constructed from this exact JSON.
		complete["encrypted_content"] = append(json.RawMessage(nil), originalEncryption...)
		if len(complete["encrypted_content"]) == 0 {
			return providerhttp.ErrProtocol
		}
		merged, err := json.Marshal(complete)
		if err != nil || len(merged) > maxReplayBytes {
			return providerhttp.ErrProtocol
		}
		final.Output[index] = merged
	}
	return nil
}
