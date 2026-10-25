package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const redirectTestAuthorization = "Bearer loopback-redirect-test-token"

type redirectSecurityProbe struct {
	sourceRequests    atomic.Int32
	sourceWithoutAuth atomic.Int32
	targetRequests    atomic.Int32
	targetWithAuth    atomic.Int32
}

func newRedirectSecurityServer(t *testing.T, status int, crossHostname bool) (*httptest.Server, *redirectSecurityProbe) {
	t.Helper()
	probe := &redirectSecurityProbe{}
	var target string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			probe.targetRequests.Add(1)
			if request.Header.Get("Authorization") != "" {
				probe.targetWithAuth.Add(1)
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("redirect target must not receive MCP requests"))
			return
		}
		probe.sourceRequests.Add(1)
		if request.Header.Get("Authorization") != redirectTestAuthorization {
			probe.sourceWithoutAuth.Add(1)
		}
		http.Redirect(writer, request, target, status)
	}))
	t.Cleanup(server.Close)
	target = server.URL + "/target"
	if crossHostname {
		target = strings.Replace(target, "127.0.0.1", "localhost", 1)
	}
	return server, probe
}

func newRedirectSecurityClient(t *testing.T, httpClient *http.Client, endpoint string) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: httpClient,
		EndpointValidator: EndpointValidatorFunc(func(candidate string) error {
			if candidate != endpoint {
				return errors.New("endpoint is outside the host allowlist")
			}
			return nil
		}),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{"Authorization": []string{redirectTestAuthorization}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{
		Transport: transport, ImplementationName: "redirect-security-test", ImplementationVersion: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertRedirectWasBlocked(t *testing.T, probe *redirectSecurityProbe, err error) {
	t.Helper()
	if err == nil {
		t.Error("redirecting MCP endpoint unexpectedly succeeded")
	}
	if probe.sourceRequests.Load() == 0 || probe.sourceWithoutAuth.Load() != 0 {
		t.Errorf("authorized endpoint requests=%d, requests without host authentication=%d",
			probe.sourceRequests.Load(), probe.sourceWithoutAuth.Load())
	}
	if probe.targetRequests.Load() != 0 || probe.targetWithAuth.Load() != 0 {
		t.Errorf("redirect target received %d requests, including %d with authentication",
			probe.targetRequests.Load(), probe.targetWithAuth.Load())
	}
}

func TestClientRejectsEndpointRedirects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		status        int
		crossHostname bool
	}{
		{"cross-host-301", http.StatusMovedPermanently, true},
		{"cross-host-302", http.StatusFound, true},
		{"cross-host-303", http.StatusSeeOther, true},
		{"cross-host-307", http.StatusTemporaryRedirect, true},
		{"cross-host-308", http.StatusPermanentRedirect, true},
		{"same-origin-307", http.StatusTemporaryRedirect, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, probe := newRedirectSecurityServer(t, test.status, test.crossHostname)
			hostClient := server.Client()
			endpoint := server.URL + "/mcp"
			client := newRedirectSecurityClient(t, hostClient, endpoint)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			_, err := client.DiscoverTools(ctx, endpoint)
			assertRedirectWasBlocked(t, probe, err)
			if hostClient.CheckRedirect != nil {
				t.Error("MCP adapter changed the host client's redirect policy")
			}
		})
	}
}

func TestClientRejectsToolCallRedirectAfterInitialization(t *testing.T) {
	t.Parallel()
	server := newTestLookupServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	})
	probe := &redirectSecurityProbe{}
	var toolCalls atomic.Int32
	var target string
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			probe.targetRequests.Add(1)
			if request.Header.Get("Authorization") != "" {
				probe.targetWithAuth.Add(1)
			}
			http.Error(writer, "redirect target must not execute tools", http.StatusBadRequest)
			return
		}
		probe.sourceRequests.Add(1)
		if request.Header.Get("Authorization") != redirectTestAuthorization {
			probe.sourceWithoutAuth.Add(1)
		}
		if request.Method == http.MethodPost {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, "cannot read request", http.StatusBadRequest)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			var envelope struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				http.Error(writer, "invalid MCP request", http.StatusBadRequest)
				return
			}
			if envelope.Method == "tools/call" {
				toolCalls.Add(1)
				http.Redirect(writer, request, target, http.StatusTemporaryRedirect)
				return
			}
		}
		// Complete the actual MCP handshake before redirecting the later tool request.
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(httpServer.Close)
	target = strings.Replace(httpServer.URL, "127.0.0.1", "localhost", 1) + "/target"
	endpoint := httpServer.URL + "/mcp"
	client := newRedirectSecurityClient(t, httpServer.Client(), endpoint)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := client.CallTool(ctx, endpoint, CallRequest{
		Name: testLookupTool, Arguments: json.RawMessage(`{"id":"redirect"}`),
	})
	if toolCalls.Load() != 1 {
		t.Fatalf("tool requests after initialization = %d, error = %v", toolCalls.Load(), err)
	}
	assertRedirectWasBlocked(t, probe, err)
}

func TestClientRedirectPolicyPreservesHostDenial(t *testing.T) {
	t.Parallel()
	server, probe := newRedirectSecurityServer(t, http.StatusTemporaryRedirect, true)
	hostClient := server.Client()
	hostDenied := errors.New("host redirect policy denied this request")
	var checks atomic.Int32
	hostClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		checks.Add(1)
		return hostDenied
	}
	endpoint := server.URL + "/mcp"
	client := newRedirectSecurityClient(t, hostClient, endpoint)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := client.DiscoverTools(ctx, endpoint)
	assertRedirectWasBlocked(t, probe, err)
	if !errors.Is(err, hostDenied) {
		t.Errorf("host denial was not preserved: %v", err)
	}
	if checks.Load() == 0 {
		t.Error("host redirect policy was not called")
	}
}

func TestClientRedirectPolicyCannotBeRelaxedByHost(t *testing.T) {
	t.Parallel()
	server, probe := newRedirectSecurityServer(t, http.StatusTemporaryRedirect, true)
	hostClient := server.Client()
	var checks atomic.Int32
	hostClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		checks.Add(1)
		return nil
	}
	endpoint := server.URL + "/mcp"
	client := newRedirectSecurityClient(t, hostClient, endpoint)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := client.DiscoverTools(ctx, endpoint)
	assertRedirectWasBlocked(t, probe, err)
	mcpChecks := checks.Load()
	if mcpChecks == 0 {
		t.Error("host redirect policy was not called")
	}

	// The caller's client must retain its own redirect and authentication behavior.
	before := probe.targetRequests.Load()
	authBefore := probe.targetWithAuth.Load()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := hostClient.Do(request)
	if err != nil {
		t.Fatalf("host client can no longer follow redirects: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || probe.targetRequests.Load() != before+1 {
		t.Errorf("host redirect behavior changed: status=%d, target requests=%d",
			response.StatusCode, probe.targetRequests.Load())
	}
	if checks.Load() != mcpChecks+1 {
		t.Error("host redirect callback was replaced")
	}
	if probe.targetWithAuth.Load() != authBefore {
		t.Error("host client was contaminated by MCP authentication")
	}
}
