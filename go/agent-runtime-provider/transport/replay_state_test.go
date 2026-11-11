package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesStatelessReplayPreservesOutputItems(t *testing.T) {
	body := []byte(`{
      "id":"resp_replay",
      "output":[
        {"id":"rs_1","type":"reasoning","status":"completed","encrypted_content":"cipher_1","summary":[{"type":"summary_text","text":"plan"}]},
        {"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]},
        {"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"x\"}"}
      ],
      "usage":{"input_tokens":10,"output_tokens":5}
    }`)
	output, err := parseOpenAIGenerateOutput(EndpointResponses, AdapterOpenAIResponses, body, false)
	require.NoError(t, err)
	require.NotEmpty(t, output.ReplayStateJSON)
	replay := decodeProviderReplayItems(output.ReplayStateJSON, AdapterOpenAIResponses)
	require.Len(t, replay, 3)
	require.Equal(t, "cipher_1", getString(replay[0]["encrypted_content"]))
	require.Equal(t, "call_1", getString(replay[2][valueCallIdFD3C070D]))

	input := buildResponsesAPIInput([]Message{{Role: "assistant", Content: "reconstructed text must not win", ReplayStateJSON: output.ReplayStateJSON}})
	require.Equal(t, replay, input)
}

func TestProviderReplayEnvelopeRejectsOtherProtocol(t *testing.T) {
	raw := encodeProviderReplayItems(AdapterGoogleInteractions, []interface{}{map[string]interface{}{valueTypeF2B37301: "thought", "signature": "sig"}})
	require.NotEmpty(t, raw)
	require.Nil(t, decodeProviderReplayItems(raw, AdapterOpenAIResponses))
}
