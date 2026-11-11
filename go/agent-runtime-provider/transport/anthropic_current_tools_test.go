package transport

import "testing"

func TestAnthropicCurrentHostedToolTypesAreNative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		toolType       string
		wantName       string
		wantCallersLen int
	}{
		{toolType: valueWebSearch202603182EE73157, wantName: valueWebSearchFB08AB8E, wantCallersLen: 1},
		{toolType: valueWebFetch20260318CCDD671D, wantName: valueWebFetchB439CCFC, wantCallersLen: 1},
		{toolType: valueCodeExecution20260521CE602076, wantName: valueCodeExecution86E8B41F},
	}

	for _, test := range tests {
		t.Run(test.toolType, func(t *testing.T) {
			t.Parallel()
			if !isAnthropicNativeToolType(test.toolType) {
				t.Fatalf("%s must be classified as a native hosted tool", test.toolType)
			}
			if got := anthropicNativeToolName(test.toolType); got != test.wantName {
				t.Fatalf("anthropicNativeToolName(%q) = %q, want %q", test.toolType, got, test.wantName)
			}
			callers := anthropicNativeToolAllowedCallers(test.toolType)
			if len(callers) != test.wantCallersLen {
				t.Fatalf("anthropicNativeToolAllowedCallers(%q) = %#v, want length %d", test.toolType, callers, test.wantCallersLen)
			}
		})
	}
}
