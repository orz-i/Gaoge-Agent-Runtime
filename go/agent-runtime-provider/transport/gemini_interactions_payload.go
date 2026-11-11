package transport

import (
	"encoding/json"
	"errors"
)

const geminiInlineRequestMaxBytes = 20_000_000

var errGeminiInlineRequestTooLarge = errors.New("google inline media request exceeds 20 MB including text and base64 data")

func marshalGeminiInteractionPayload(body map[string]interface{}) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(payload) > geminiInlineRequestMaxBytes {
		return nil, errGeminiInlineRequestTooLarge
	}
	return payload, nil
}
