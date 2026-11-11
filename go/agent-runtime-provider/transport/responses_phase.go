package transport

import (
	"errors"
	"strings"
)

var errInvalidResponsesPhase = errors.New("invalid or ambiguous Responses message phase")

// Missing/null phases retain the existing compatible-endpoint projection.
// Once phase is explicit, commentary is intermediate and cannot certify a final.
func projectResponsesPhase(items []any) (string, bool, bool, error) {
	var commentary strings.Builder
	var final string
	var phased, legacy, hasFinal, hasCall, refused bool
	for _, raw := range items {
		item := asMap(raw)
		kind := getString(item["type"])
		if isResponsesClientToolCallType(kind) {
			hasCall = true
			continue
		}
		if kind != "message" {
			continue
		}
		phase, explicit, err := responsesMessagePhase(item)
		if err != nil {
			return "", false, false, err
		}
		for _, rawPart := range asSlice(item["content"]) {
			if getString(asMap(rawPart)["type"]) == "refusal" {
				refused = true
			}
		}
		if !explicit {
			legacy = true
			continue
		}
		phased = true
		if role, present := item["role"]; present && role != "assistant" {
			return "", false, false, errInvalidResponsesPhase
		}
		if status, present := item["status"]; present && status != "completed" {
			return "", false, false, errInvalidResponsesPhase
		}
		text, err := responsesPhaseMessageText(item)
		if err != nil {
			return "", true, refused, err
		}
		switch phase {
		case "commentary":
			if hasFinal {
				return "", false, false, errInvalidResponsesPhase
			}
			commentary.WriteString(text)
		case "final_answer":
			if hasFinal {
				return "", false, false, errInvalidResponsesPhase
			}
			hasFinal = true
			final = text
		}
	}
	if !phased {
		if refused && hasCall {
			return "", false, refused, errInvalidResponsesPhase
		}
		return "", false, refused, nil
	}
	// Refusal is global across phases, even when final content is valid JSON.
	if legacy || (refused && hasCall) || (hasFinal && hasCall) || (!hasFinal && !hasCall) {
		return "", true, refused, errInvalidResponsesPhase
	}
	if hasFinal {
		if final == "" {
			return "", true, refused, errInvalidResponsesPhase
		}
		return final, true, refused, nil
	}
	return commentary.String(), true, refused, nil
}

func responsesMessagePhase(item map[string]any) (string, bool, error) {
	raw, present := item["phase"]
	if !present || raw == nil {
		return "", false, nil
	}
	phase, ok := raw.(string)
	if !ok || (phase != "commentary" && phase != "final_answer") {
		return "", true, errInvalidResponsesPhase
	}
	return phase, true, nil
}

func rememberResponsesPhase(result *GenerateOutput, item map[string]any, index int) error {
	if getString(item["type"]) != "message" {
		return nil
	}
	phase, _, err := responsesMessagePhase(item)
	if err != nil {
		return err
	}
	if result.responsesPhases == nil {
		result.responsesPhases = map[int]string{}
		result.responsesItemPhases = map[string]string{}
		result.responsesItemIndexes = map[string]int{}
	}
	if prior, present := result.responsesPhases[index]; present && prior != phase {
		return errInvalidResponsesPhase
	}
	result.responsesPhases[index] = phase
	if id := getString(item["id"]); id != "" {
		if prior, present := result.responsesItemPhases[id]; present && prior != phase {
			return errInvalidResponsesPhase
		}
		result.responsesItemPhases[id] = phase
		result.responsesItemIndexes[id] = index
	}
	return nil
}

func responsesEventPhase(result *GenerateOutput, event map[string]any) (string, error) {
	if id := getString(event["item_id"]); id != "" {
		if phase, present := result.responsesItemPhases[id]; present {
			return phase, nil
		}
		return "pending", nil
	}
	index, err := streamIndexField(event, "output_index")
	if err != nil {
		return "", err
	}
	phase, known := result.responsesPhases[index]
	if !known {
		return "pending", nil
	}
	return phase, nil
}

func responsesPhaseMessageText(item map[string]any) (string, error) {
	parts, ok := item["content"].([]any)
	if !ok || parts == nil {
		return "", errInvalidResponsesPhase
	}
	var text strings.Builder
	for _, raw := range parts {
		part := asMap(raw)
		var key string
		switch getString(part["type"]) {
		case "output_text":
			key = "text"
		case "refusal":
			key = "refusal"
		default:
			return "", errInvalidResponsesPhase
		}
		value, ok := part[key].(string)
		if !ok || (key == "refusal" && value == "") {
			return "", errInvalidResponsesPhase
		}
		text.WriteString(value)
	}
	return text.String(), nil
}

func responsesTextPartKey(result *GenerateOutput, event map[string]any) ([2]int, error) {
	index, err := streamIndexField(event, "output_index")
	if err != nil {
		return [2]int{}, err
	}
	if id := getString(event["item_id"]); id != "" {
		if known, ok := result.responsesItemIndexes[id]; ok {
			index = known
		}
	}
	content, err := streamIndexField(event, "content_index")
	if err != nil {
		return [2]int{}, err
	}
	return [2]int{index, content}, nil
}
