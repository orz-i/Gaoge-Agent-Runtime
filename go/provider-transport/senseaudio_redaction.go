package transport

import (
	"encoding/json"
	"errors"
)

// Speech responses may contain megabytes of hex audio. Never attach them to
// upstream errors, diagnostic snapshots, or persistent trace payloads.
func redactSenseAudioSpeechError(err error, debug *UpstreamDebugSnapshot) {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		upstream.Body = "[redacted: speech response]"
		if upstream.Debug != nil {
			upstream.Debug.Response.Body = upstream.Body
		}
	}
	if debug != nil {
		debug.Response.Body = "[redacted: speech response]"
	}
}

func redactSenseAudioSpeechDebug(debug *UpstreamDebugSnapshot, safeBody []byte) {
	if debug != nil {
		debug.Response.Body = string(safeBody)
	}
}

func senseAudioSafeSpeechResponse(parsed map[string]interface{}) []byte {
	redacted := make(map[string]interface{}, len(parsed))
	for key, value := range parsed {
		redacted[key] = value
	}
	if data, ok := parsed["data"].(map[string]interface{}); ok {
		safeData := make(map[string]interface{}, len(data))
		for key, value := range data {
			safeData[key] = value
		}
		if _, containsAudio := safeData["audio"]; containsAudio {
			safeData["audio"] = "[redacted]"
		}
		redacted["data"] = safeData
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
