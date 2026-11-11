package transport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These numeric frames reproduce C2-L1 supplier receipts. No provider is called.
func TestAnthropicFinalUsageReplacesCumulativeFields(t *testing.T) {
	for _, sample := range []struct{ input, cached, output int64 }{
		{2342, 128, 590}, {1715, 128, 386}, {7138, 1275, 694},
		{974178, 128, 115}, {973729, 128, 116},
	} {
		t.Run(fmt.Sprint(sample.input), func(t *testing.T) {
			stream := fmt.Sprintf("data: {\"type\":\"message_start\",\"message\":{\"id\":\"captured\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":%d,\"cache_read_input_tokens\":%d,\"output_tokens\":%d}}\n\ndata: {\"type\":\"message_stop\"}\n\n", sample.input, sample.cached, sample.output)
			var output GenerateOutput
			var last Usage
			require.NoError(t, consumeAnthropicStream(strings.NewReader(stream), &output, func(event GenerateStreamEvent) error { last = event.Usage; return nil }, newAnthropicToolClassifier(nil, nil)))
			require.Equal(t, sample.input, output.Usage.InputTokens)
			require.Equal(t, sample.cached, output.Usage.CacheReadTokens)
			require.Equal(t, sample.output, output.Usage.OutputTokens)
			require.Equal(t, output.Usage, last)
		})
	}
}

func TestAnthropicUsageDeltaPreservesOmittedAndAcceptsExplicitZero(t *testing.T) {
	output := &GenerateOutput{Usage: parseAnthropicUsage(mustDecodeObject(t, `{"usage":{"input_tokens":32,"output_tokens":3,"cache_read_input_tokens":8,"cache_creation_input_tokens":12,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":7}}}`))}
	for _, frame := range []string{`{"usage":{"output_tokens":9}}`, `{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}`} {
		require.NoError(t, applyAnthropicMessageDelta(mustDecodeObject(t, frame), output, nil))
	}
	require.Zero(t, output.Usage.InputTokens)
	require.Zero(t, output.Usage.CacheReadTokens)
	require.Zero(t, output.Usage.CacheWriteTokens)
	require.Zero(t, output.Usage.CacheWrite5mTokens)
	require.Zero(t, output.Usage.CacheWrite1hTokens)
	require.EqualValues(t, 9, output.Usage.OutputTokens)
	require.Contains(t, output.Usage.RawUsageJSON, `"input_tokens":0`)
}
