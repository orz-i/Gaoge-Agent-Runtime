package openai

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	phaseCommentary  = "commentary"
	phaseFinalAnswer = "final_answer"
)

// outputProjection is shared by response decoding and replay validation so
// phase selection cannot change the meaning of an already committed message.
type outputProjection struct {
	content   string
	refused   bool
	phased    bool
	reasoning bool
	calls     []outputItem
	messages  []json.RawMessage
}

func projectOutput(outputs []json.RawMessage) (outputProjection, error) {
	var result outputProjection
	var legacy, commentary strings.Builder
	var final string
	var sawLegacy, sawFinal, missingEncrypted bool
	for _, raw := range outputs {
		var item outputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return outputProjection{}, ErrUnsupported
		}
		switch item.Type {
		case "message":
			// A completed outer Responses envelope does not certify that
			// every physical message is a completed assistant output. An
			// explicit foreign role or unfinished status must invalidate
			// the entire candidate, including any sibling Tool calls.
			// Only genuinely omitted legacy fields remain supported.
			var authority struct {
				Role   json.RawMessage `json:"role"`
				Status json.RawMessage `json:"status"`
			}
			if json.Unmarshal(raw, &authority) != nil {
				return outputProjection{}, ErrUnsupported
			}
			if len(authority.Role) != 0 {
				var role string
				if json.Unmarshal(authority.Role, &role) != nil || role != string(model.RoleAssistant) {
					return outputProjection{}, ErrUnsupported
				}
			}
			if len(authority.Status) != 0 {
				var status string
				if json.Unmarshal(authority.Status, &status) != nil || status != "completed" {
					return outputProjection{}, ErrUnsupported
				}
			}
			text, refused, err := projectMessageText(item)
			if err != nil {
				return outputProjection{}, err
			}
			result.refused = result.refused || refused
			result.messages = append(result.messages, raw)
			if item.Phase == nil {
				sawLegacy = true
				legacy.WriteString(text)
				continue
			}
			result.phased = true
			switch *item.Phase {
			case phaseCommentary:
				if sawFinal {
					return outputProjection{}, ErrUnsupported
				}
				commentary.WriteString(text)
			case phaseFinalAnswer:
				if sawFinal {
					return outputProjection{}, ErrUnsupported
				}
				sawFinal, final = true, text
			default:
				return outputProjection{}, ErrUnsupported
			}
		case "function_call":
			// Preserve exact physical Call ID identity even when projecting
			// replay evidence without the earlier response-item fence.
			if item.CallID == "" || strings.TrimSpace(item.CallID) != item.CallID ||
				item.Name == "" || providerhttp.ValidateUniqueJSONKeys([]byte(item.Arguments)) != nil {
				return outputProjection{}, ErrUnsupported
			}
			result.calls = append(result.calls, item)
		case "reasoning":
			// Final response completion cannot certify an explicitly unfinished
			// physical reasoning item. An encrypted blob is replay evidence,
			// not an override for its own status; such an item must never
			// accompany an executable local function_call.
			var authority struct {
				Status json.RawMessage `json:"status"`
			}
			if json.Unmarshal(raw, &authority) != nil {
				return outputProjection{}, ErrUnsupported
			}
			if len(authority.Status) != 0 {
				var status string
				if json.Unmarshal(authority.Status, &status) != nil || status != "completed" {
					return outputProjection{}, ErrUnsupported
				}
			}
			result.reasoning = true
			missingEncrypted = missingEncrypted || item.EncryptedContent == ""
		default:
			return outputProjection{}, ErrUnsupported
		}
	}
	if result.phased {
		if sawLegacy || sawFinal && len(result.calls) != 0 || !sawFinal && len(result.calls) == 0 {
			return outputProjection{}, ErrUnsupported
		}
		result.content = final
		if !sawFinal {
			// A tool step is intermediate: retain its commentary in the durable
			// transcript as well as its original phase in provider replay.
			result.content = commentary.String()
		}
	} else {
		result.content = legacy.String()
	}
	if result.refused && len(result.calls) != 0 || result.content == "" && len(result.calls) == 0 {
		return outputProjection{}, ErrUnsupported
	}
	if result.reasoning && len(result.calls) != 0 && missingEncrypted {
		return outputProjection{}, errors.Join(ErrUnsupported, ErrReplayUnavailable)
	}
	return result, nil
}

// terminalSafetyRefusal is deliberately narrower than the global
// Response.Refused flag: projection marks refusals in *any* assistant phase
// to prevent hiding safety evidence, but only a refusal-only selected final
// message can waive a mandatory Tool call. projectOutput already validated
// phase ordering and the shape of every item before this check runs.
// For unphased legacy output, the last visible message is the final one.
func terminalSafetyRefusal(outputs []json.RawMessage) bool {
	var terminal outputItem
	var terminalRaw json.RawMessage
	for _, raw := range outputs {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil {
			return false
		}
		if item.Type == "message" && (item.Phase == nil || *item.Phase == phaseFinalAnswer) {
			terminal = item
			terminalRaw = raw
		}
	}
	if len(terminal.Content) == 0 {
		return false
	}
	// Role and status are *presence-sensitive* for the required-Tool safety
	// exception. Only omitted fields retain legacy behavior; explicit null,
	// empty, foreign roles or unfinished statuses cannot certify a terminal
	// assistant refusal. Keep the ordinary projection semantics unchanged.
	var authority struct {
		Role   json.RawMessage `json:"role"`
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(terminalRaw, &authority) != nil {
		return false
	}
	if len(authority.Role) != 0 {
		var role string
		if json.Unmarshal(authority.Role, &role) != nil || role != string(model.RoleAssistant) {
			return false
		}
	}
	if len(authority.Status) != 0 {
		var status string
		if json.Unmarshal(authority.Status, &status) != nil || status != "completed" {
			return false
		}
	}
	for _, part := range terminal.Content {
		if part.Type != "refusal" || strings.TrimSpace(part.Refusal) == "" {
			return false
		}
	}
	return true
}

func projectMessageText(item outputItem) (string, bool, error) {
	var text strings.Builder
	var refused bool
	for _, part := range item.Content {
		switch part.Type {
		case "output_text":
			text.WriteString(part.Text)
		case "refusal":
			if part.Refusal == "" {
				return "", false, ErrUnsupported
			}
			refused = true
			text.WriteString(part.Refusal)
		default:
			return "", false, ErrUnsupported
		}
	}
	return text.String(), refused, nil
}
