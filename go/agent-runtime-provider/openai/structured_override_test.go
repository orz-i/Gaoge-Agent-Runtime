package openai_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
)

func TestHostVerifiedGatewayStrictOutputCapability(t *testing.T) {
	client, err := openai.New(openai.Config{
		APIKey: "test", DefaultModel: "private/verified",
		StructuredOutputModels: []string{"private/verified"},
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := client.Capabilities(t.Context(), "private/verified")
	if err != nil || !capabilities.StructuredJSONSchema {
		t.Fatalf("explicit gateway capability not honored: %+v err=%v", capabilities, err)
	}
	for _, id := range []string{"private/other", "gpt-5-unverified", "gpt-4.1-private", "gpt-4o-mini-unknown"} {
		unknown, err := client.Capabilities(t.Context(), id)
		if err != nil || unknown.StructuredJSONSchema {
			t.Fatalf("unverified model %q capability leaked: %+v err=%v", id, unknown, err)
		}
	}
}
