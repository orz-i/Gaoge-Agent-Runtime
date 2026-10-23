package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider"
)

type capableRecorder struct {
	recorder
	checked string
}

func (r *capableRecorder) Capabilities(_ context.Context, id string) (model.Capabilities, error) {
	r.checked = id
	return model.Capabilities{StructuredJSONSchema: id == "claude-sonnet-5-5"}, nil
}
func TestCapabilityRouterChecksExactRoutedModel(t *testing.T) {
	supported := &capableRecorder{}
	unsupported := &recorder{}
	router, err := provider.NewRouter("a", provider.Binding{Name: "a", Client: unsupported}, provider.Binding{Name: "b", Client: supported})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := router.Capabilities(t.Context(), "b/claude-sonnet-5-5")
	if err != nil || !caps.StructuredJSONSchema || supported.checked != "claude-sonnet-5-5" {
		t.Fatalf("capability request not routed: %+v %v checked=%q", caps, err, supported.checked)
	}
	caps, err = router.Capabilities(t.Context(), "b/claude-unknown")
	if err != nil || caps.StructuredJSONSchema {
		t.Fatalf("unsupported model admitted: %+v %v", caps, err)
	}
	_, err = router.Capabilities(t.Context(), "a/any")
	if !errors.Is(err, model.ErrCapabilityUnavailable) {
		t.Fatalf("missing capability provider accepted: %v", err)
	}
	_, err = router.Capabilities(t.Context(), "unknown/any")
	if !errors.Is(err, provider.ErrInvalidRouting) {
		t.Fatalf("unregistered route accepted: %v", err)
	}
}
