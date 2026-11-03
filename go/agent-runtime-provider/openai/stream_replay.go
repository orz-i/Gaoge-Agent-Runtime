package openai

import (
	"bytes"
	"encoding/json"
	"reflect"
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
		if err != nil || !equalJSON(doneBase, completeBase) {
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

// completedToolArguments records finalized argument evidence independently of
// output_item.done, then reconciles it before any executable call is emitted.
type completedToolArguments struct {
	byIndex map[int]struct{ id, arguments string }
	size    int
}

func (a *completedToolArguments) observe(index *int, id string, arguments *string) error {
	if index == nil || *index < 0 || *index >= maxReplayItems || id == "" || strings.TrimSpace(id) != id || arguments == nil || a.size+len(id)+len(*arguments) > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	if a.byIndex == nil {
		a.byIndex = make(map[int]struct{ id, arguments string })
	}
	if _, exists := a.byIndex[*index]; exists {
		return providerhttp.ErrProtocol
	}
	a.byIndex[*index] = struct{ id, arguments string }{id, *arguments}
	a.size += len(id) + len(*arguments)
	return nil
}
func (a *completedToolArguments) reconcile(final *responseBody) error {
	for index, evidence := range a.byIndex {
		if index >= len(final.Output) {
			return providerhttp.ErrProtocol
		}
		var item struct {
			Type      string  `json:"type"`
			ID        string  `json:"id"`
			Arguments *string `json:"arguments"`
		}
		if json.Unmarshal(final.Output[index], &item) != nil || item.Type != "function_call" || item.ID != evidence.id || item.Arguments == nil || *item.Arguments != evidence.arguments {
			return providerhttp.ErrProtocol
		}
	}
	return nil
}

// Completed message-part and refusal events are independent terminal evidence.
type contentKey struct{ output, content int }
type contentEvidence struct {
	id      string
	part    json.RawMessage
	refusal *string
	text    *string
}
type completedMessageContent struct {
	byKey map[contentKey]contentEvidence
	size  int
}

func (c *completedMessageContent) entry(output, content *int, id string) (contentKey, contentEvidence, error) {
	if output == nil || content == nil || *output < 0 || *output >= maxReplayItems || *content < 0 || *content >= maxReplayItems || id == "" || strings.TrimSpace(id) != id {
		return contentKey{}, contentEvidence{}, providerhttp.ErrProtocol
	}
	if c.byKey == nil {
		c.byKey = make(map[contentKey]contentEvidence)
	}
	key := contentKey{*output, *content}
	e := c.byKey[key]
	if e.id != "" && e.id != id {
		return key, e, providerhttp.ErrProtocol
	}
	e.id = id
	return key, e, nil
}
func (c *completedMessageContent) observeRefusal(output, content *int, id string, refusal *string) error {
	key, e, err := c.entry(output, content, id)
	if err != nil || refusal == nil || e.refusal != nil {
		return providerhttp.ErrProtocol
	}
	c.size += len(id) + len(*refusal)
	if c.size > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	e.refusal = refusal
	c.byKey[key] = e
	return nil
}
func (c *completedMessageContent) observeText(output, content *int, id string, text *string) error {
	key, e, err := c.entry(output, content, id)
	if err != nil || text == nil || e.text != nil {
		return providerhttp.ErrProtocol
	}
	c.size += len(id) + len(*text)
	if c.size > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	e.text = text
	c.byKey[key] = e
	return nil
}
func (c *completedMessageContent) observePart(output, content *int, id string, part json.RawMessage) error {
	key, e, err := c.entry(output, content, id)
	if err != nil || len(part) == 0 || e.part != nil || string(part) == "null" {
		return providerhttp.ErrProtocol
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(part, &obj) != nil || obj == nil {
		return providerhttp.ErrProtocol
	}
	c.size += len(id) + len(part)
	if c.size > maxReplayBytes {
		return providerhttp.ErrProtocol
	}
	e.part = append(json.RawMessage(nil), part...)
	c.byKey[key] = e
	return nil
}
func (c *completedMessageContent) reconcile(final *responseBody) error {
	for key, e := range c.byKey {
		if key.output >= len(final.Output) {
			return providerhttp.ErrProtocol
		}
		var item struct {
			Type    string            `json:"type"`
			ID      string            `json:"id"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(final.Output[key.output], &item) != nil || item.Type != "message" || item.ID != e.id || key.content >= len(item.Content) {
			return providerhttp.ErrProtocol
		}
		var part map[string]json.RawMessage
		if json.Unmarshal(item.Content[key.content], &part) != nil || part == nil {
			return providerhttp.ErrProtocol
		}
		if e.part != nil {
			var done map[string]json.RawMessage
			if json.Unmarshal(e.part, &done) != nil {
				return providerhttp.ErrProtocol
			}
			x, err := json.Marshal(done)
			if err != nil {
				return providerhttp.ErrProtocol
			}
			y, err := json.Marshal(part)
			if err != nil || !equalJSON(x, y) {
				return providerhttp.ErrProtocol
			}
		}
		if e.text != nil {
			var typ, text string
			if json.Unmarshal(part["type"], &typ) != nil || typ != "output_text" || json.Unmarshal(part["text"], &text) != nil || text != *e.text {
				return providerhttp.ErrProtocol
			}
		}
		if e.refusal != nil {
			var typ, refusal string
			if json.Unmarshal(part["type"], &typ) != nil || typ != "refusal" || json.Unmarshal(part["refusal"], &refusal) != nil || refusal != *e.refusal {
				return providerhttp.ErrProtocol
			}
		}
	}
	return nil
}

// equalJSON ignores object key order at every depth while preserving numeric text.
func equalJSON(a, b []byte) bool {
	var left, right any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	return da.Decode(&left) == nil && db.Decode(&right) == nil && reflect.DeepEqual(left, right)
}
