package agentruntime

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceRegistryFreezesProviderSet(t *testing.T) {
	first := &scriptedWorkspace{}
	second := &scriptedWorkspace{}
	providers := map[string]WorkspaceProvider{"novel": first}
	registry := NewWorkspaceRegistry(providers)
	providers["novel"] = second
	providers["notes"] = second

	resolved, ok := registry.ResolveWorkspace(" novel ")
	if !ok || resolved != first {
		t.Fatalf("resolved provider = %#v, %t", resolved, ok)
	}
	if _, ok = registry.ResolveWorkspace("notes"); ok {
		t.Fatal("registry observed a post-construction mutation")
	}
}

func TestWorkspaceEnvelopeIsProviderNeutralAndV1(t *testing.T) {
	var request WorkspaceRequest
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"type":"novel"}`), &request); err != nil {
		t.Fatalf("generic provider envelope rejected: %v", err)
	}
	for _, payload := range []string{`{"schemaVersion":7,"type":"novel"}`, `{"schemaVersion":1,"type":""}`} {
		if err := json.Unmarshal([]byte(payload), &request); err == nil {
			t.Fatalf("invalid workspace envelope accepted: %s", payload)
		}
	}
}
