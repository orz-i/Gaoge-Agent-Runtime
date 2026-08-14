package a2a

import (
	"context"
	"net/http"
	"testing"
)

const testAuthorization = "Bearer token"

func TestTransportRequiresHostNetworkPolicyAndClonesHeaders(t *testing.T) {
	t.Parallel()
	if _, err := NewTransport(TransportDependencies{}); err == nil {
		t.Fatal("expected missing host dependencies to fail")
	}
	headers := http.Header{"Authorization": []string{testAuthorization}}
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: &http.Client{},
		EndpointValidator: EndpointValidatorFunc(func(endpoint string) error {
			if endpoint != "https://agent.example/a2a" {
				t.Fatalf("endpoint = %q", endpoint)
			}
			return nil
		}),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) { return headers, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, prepared, err := transport.prepare(t.Context(), " https://agent.example/a2a ")
	if err != nil || endpoint != "https://agent.example/a2a" || prepared.Get("Authorization") != testAuthorization {
		t.Fatalf("endpoint=%q headers=%v err=%v", endpoint, prepared, err)
	}
	prepared.Set("Authorization", "changed")
	if headers.Get("Authorization") != testAuthorization {
		t.Fatal("prepared headers mutated host headers")
	}
}

func TestRemoteAgentDescriptorCloneIsolated(t *testing.T) {
	t.Parallel()
	original := RemoteAgentDescriptor{Capabilities: []string{"streaming", "tasks"}}
	clone := CloneRemoteAgentDescriptor(original)
	clone.Capabilities[0] = "changed"
	if original.Capabilities[0] != "streaming" {
		t.Fatal("descriptor clone mutated source")
	}
}
