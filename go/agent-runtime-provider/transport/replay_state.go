package transport

import (
	"bytes"
	"encoding/json"
	"strings"
)

type providerReplayEnvelope struct {
	Protocol string            `json:"protocol"`
	Items    []json.RawMessage `json:"items"`
}

func encodeProviderReplayItems(protocol string, items []interface{}) string {
	protocol = NormalizeAdapter(protocol)
	if protocol == "" || len(items) == 0 {
		return ""
	}
	envelope := providerReplayEnvelope{Protocol: protocol, Items: make([]json.RawMessage, 0, len(items))}
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		envelope.Items = append(envelope.Items, raw)
	}
	if len(envelope.Items) == 0 {
		return ""
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeProviderReplayItems(raw string, protocol string) []map[string]interface{} {
	raw = strings.TrimSpace(raw)
	protocol = NormalizeAdapter(protocol)
	if raw == "" || protocol == "" {
		return nil
	}
	var envelope providerReplayEnvelope
	if json.Unmarshal([]byte(raw), &envelope) != nil || NormalizeAdapter(envelope.Protocol) != protocol {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		decoder := json.NewDecoder(bytes.NewReader(item))
		decoder.UseNumber()
		var value map[string]interface{}
		if decoder.Decode(&value) != nil || len(value) == 0 {
			return nil
		}
		result = append(result, value)
	}
	return result
}

func providerReplayItemsAsInterfaces(items []map[string]interface{}) []interface{} {
	result := make([]interface{}, 0, len(items))
	for _, item := range items {
		if len(item) != 0 {
			result = append(result, cloneMap(item))
		}
	}
	return result
}
