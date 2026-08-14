package mcp

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
			if endpoint != "https://mcp.example/rpc" {
				t.Fatalf("endpoint = %q", endpoint)
			}
			return nil
		}),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) { return headers, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, prepared, err := transport.prepare(t.Context(), " https://mcp.example/rpc ")
	if err != nil || endpoint != "https://mcp.example/rpc" || prepared.Get("Authorization") != testAuthorization {
		t.Fatalf("endpoint=%q headers=%v err=%v", endpoint, prepared, err)
	}
	prepared.Set("Authorization", "changed")
	if headers.Get("Authorization") != testAuthorization {
		t.Fatal("prepared headers mutated host headers")
	}
}

func TestTransportRejectsCredentialsInEndpoint(t *testing.T) {
	t.Parallel()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: &http.Client{}, EndpointValidator: EndpointValidatorFunc(func(string) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = transport.prepare(t.Context(), "https://user:secret@mcp.example/rpc"); err == nil {
		t.Fatal("expected embedded credentials to fail")
	}
}
