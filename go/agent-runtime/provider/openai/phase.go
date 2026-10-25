package openai

import (
	"encoding/json"
	"errors"
	"strings"

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
			if item.Phase != nil && item.Role != "" && item.Role != string(model.RoleAssistant) {
				return outputProjection{}, ErrUnsupported
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
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return outputProjection{}, ErrUnsupported
			}
			result.calls = append(result.calls, item)
		case "reasoning":
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
