package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
)

// completedOutputItems collects finished reasoning, message and local Tool
// items from response.output_item.done. The final response must reconcile
// with those events before granting Tool authority or replay. Earlier added
// items are not completion proof: encrypted reasoning may still be partial.
type completedOutputItems struct {
	byIndex map[int]json.RawMessage
	size    int
}

func (items *completedOutputItems) observe(index *int, raw json.RawMessage) error {
	var identity struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &identity) != nil {
		return providerhttp.ErrProtocol
	}
	if identity.Type != "reasoning" && identity.Type != "function_call" && identity.Type != "message" {
		// A completed item is physical output evidence, not an ignorable
		// progress hint. A future/unsupported item cannot be silently
		// replaced by a locally executable Tool in response.completed.
		return providerhttp.ErrProtocol
	}
	if index == nil || *index < 0 || *index >= maxReplayItems ||
		identity.Type == "reasoning" && identity.ID == "" ||
		items.size+len(raw) > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	if identity.Type == "function_call" {
		var call struct {
			Status    json.RawMessage `json:"status"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
		}
		if json.Unmarshal(raw, &call) != nil || call.CallID == "" ||
			strings.TrimSpace(call.CallID) != call.CallID || call.Name == "" ||
			providerhttp.ValidateUniqueJSONObject([]byte(call.Arguments)) != nil {
			return providerhttp.ErrProtocol
		}
		if len(call.Status) != 0 {
			var status string
			if json.Unmarshal(call.Status, &status) != nil || status != "completed" {
				return providerhttp.ErrProtocol
			}
		}
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

// reconcile proves that observed completed output items and the final SSE
// response agree in type, identity, arguments, position and completion status.
// Reasoning alone may supply the encrypted bytes omitted by the final frame;
// its existing stricter status evidence is preserved. Mismatches fail closed.
func (items *completedOutputItems) reconcile(final *responseBody) error {
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
			doneIdentity.Type != finalIdentity.Type ||
			doneIdentity.Type == "reasoning" &&
				(doneIdentity.ID == "" || doneIdentity.ID != finalIdentity.ID) {
			return providerhttp.ErrProtocol
		}
		// Tool items must match in full; only reasoning items may provide
		// separate encrypted bytes missing from the final response.
		originalEncryption := done["encrypted_content"]
		if doneIdentity.Type == "reasoning" {
			delete(done, "encrypted_content")
			delete(complete, "encrypted_content")
		}
		// A completed status may be omitted. For Tool/Message, accept the
		// omission on either side; Reasoning retains its older, stricter
		// completed-done status rule. Explicit incomplete/in_progress/null
		// or conflicting status evidence is never authoritative.
		if rawStatus, present := done["status"]; present {
			var status string
			if json.Unmarshal(rawStatus, &status) != nil || status != "completed" {
				return providerhttp.ErrProtocol
			}
			if _, present := complete["status"]; !present {
				delete(done, "status")
			}
		} else if rawStatus, present := complete["status"]; present && doneIdentity.Type != "reasoning" {
			var status string
			if json.Unmarshal(rawStatus, &status) != nil || status != "completed" {
				return providerhttp.ErrProtocol
			}
			delete(complete, "status")
		}
		doneBase, err := json.Marshal(done)
		if err != nil {
			return providerhttp.ErrProtocol
		}
		completeBase, err := json.Marshal(complete)
		if err != nil || !bytes.Equal(doneBase, completeBase) {
			return providerhttp.ErrProtocol
		}
		if doneIdentity.Type != "reasoning" {
			continue
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
