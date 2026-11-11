package transport

// SupportsNativeInputTokenCounting selects documented MiniMax tokenizer endpoints
// for the same wire protocol as generation; Chat Completions has no such endpoint.
func SupportsNativeInputTokenCounting(protocol, model string) bool {
	if model != "MiniMax-M3" && model != "MiniMax-M3.1-Flash-Preview" {
		return false
	}
	switch NormalizeAdapter(protocol) {
	case AdapterOpenAIResponses, AdapterAnthropicMessages, AdapterMiniMaxAnthropic:
		return true
	default:
		return false
	}
}
