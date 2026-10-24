package a2a

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const redirectTestAuthorization = "Bearer a2a-redirect-fixture"

func TestClientRejectsHTTPRedirects(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		for _, operation := range []string{"discover", "send"} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				fixture := newA2ARedirectFixture(t, status, true)
				client := newA2ARedirectClient(t, fixture.server.Client(), fixture.server.URL)
				err := callA2ARedirectOperation(t, client, fixture.server.URL, operation)
				fixture.assertBlocked(t, err)
			})
		}
	}
}

func TestClientRejectsSameOriginRedirects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		operation string
		status    int
	}{
		{operation: "discover", status: http.StatusFound},
		{operation: "send", status: http.StatusTemporaryRedirect},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			fixture := newA2ARedirectFixture(t, tc.status, false)
			client := newA2ARedirectClient(t, fixture.server.Client(), fixture.server.URL)
			err := callA2ARedirectOperation(t, client, fixture.server.URL, tc.operation)
			fixture.assertBlocked(t, err)
		})
	}
}

func TestClientRedirectPolicyPreservesHostClient(t *testing.T) {
	t.Parallel()
	hostDenied := errors.New("host denied redirect")
	for _, tc := range []struct {
		name      string
		callback  bool
		policyErr error
	}{
		{name: "default"},
		{name: "allow", callback: true},
		{name: "deny", callback: true, policyErr: hostDenied},
		{name: "last_response", callback: true, policyErr: http.ErrUseLastResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newA2ARedirectFixture(t, http.StatusFound, false)
			hostClient := fixture.server.Client()
			var policyCalls atomic.Int32
			if tc.callback {
				hostClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
					policyCalls.Add(1)
					if request.URL.String() != fixture.targetURL || len(via) != 1 {
						t.Errorf("redirect callback target=%q history=%d", request.URL, len(via))
					}
					return tc.policyErr
				}
			}

			client := newA2ARedirectClient(t, hostClient, fixture.server.URL)
			_, err := client.Discover(t.Context(), fixture.server.URL)
			fixture.assertBlocked(t, err)
			if errors.Is(tc.policyErr, hostDenied) && !errors.Is(err, hostDenied) {
				t.Errorf("discovery error = %v, want original host denial", err)
			}
			if tc.callback && policyCalls.Load() != 1 {
				t.Errorf("host redirect callback calls = %d, want 1", policyCalls.Load())
			}
			if !tc.callback && hostClient.CheckRedirect != nil {
				t.Error("protocol adapter changed the host redirect callback")
			}

			// The caller's client must retain its original redirect behavior.
			response, err := hostClient.Get(fixture.server.URL)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if errors.Is(tc.policyErr, hostDenied) {
				if !errors.Is(err, hostDenied) {
					t.Fatalf("host client error = %v, want original denial", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := http.StatusNoContent
				if errors.Is(tc.policyErr, http.ErrUseLastResponse) {
					wantStatus = http.StatusFound
				}
				if response.StatusCode != wantStatus {
					t.Errorf("host client status = %d, want %d", response.StatusCode, wantStatus)
				}
			}
			if tc.callback && policyCalls.Load() != 2 {
				t.Errorf("host redirect callback calls = %d, want 2", policyCalls.Load())
			}
			wantTargetCalls := int32(0)
			if tc.policyErr == nil {
				wantTargetCalls = 1
			}
			if got := fixture.targetCalls.Load(); got != wantTargetCalls {
				t.Errorf("target calls after host request = %d, want %d", got, wantTargetCalls)
			}
			if fixture.targetAuthorized.Load() {
				t.Error("redirect target received protocol authentication")
			}
		})
	}
}

type a2aRedirectFixture struct {
	server           *httptest.Server
	targetURL        string
	sourceCalls      atomic.Int32
	sourceAuthorized atomic.Bool
	targetCalls      atomic.Int32
	targetAuthorized atomic.Bool
}

func newA2ARedirectFixture(t *testing.T, status int, crossHostname bool) *a2aRedirectFixture {
	t.Helper()
	fixture := &a2aRedirectFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect-target" {
			fixture.targetCalls.Add(1)
			if request.Header.Get("Authorization") != "" {
				fixture.targetAuthorized.Store(true)
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		fixture.sourceCalls.Add(1)
		if request.Header.Get("Authorization") == redirectTestAuthorization {
			fixture.sourceAuthorized.Store(true)
		}
		writer.Header().Set("Location", fixture.targetURL)
		writer.WriteHeader(status)
	}))
	t.Cleanup(fixture.server.Close)
	fixture.targetURL = fixture.server.URL + "/redirect-target"
	if crossHostname {
		fixture.targetURL = strings.Replace(fixture.targetURL, "127.0.0.1", "localhost", 1)
	}
	return fixture
}

func (fixture *a2aRedirectFixture) assertBlocked(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Error("A2A operation accepted a redirect response")
	}
	if got := fixture.sourceCalls.Load(); got != 1 || !fixture.sourceAuthorized.Load() {
		t.Errorf("validated endpoint calls=%d authenticated=%t, want one authenticated request",
			got, fixture.sourceAuthorized.Load())
	}
	if got := fixture.targetCalls.Load(); got != 0 {
		t.Errorf("redirect target received %d requests, want none", got)
	}
	if fixture.targetAuthorized.Load() {
		t.Error("redirect target received protocol authentication")
	}
}

func newA2ARedirectClient(t *testing.T, httpClient *http.Client, endpoint string) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: httpClient,
		EndpointValidator: EndpointValidatorFunc(func(candidate string) error {
			if candidate != endpoint {
				return ErrInvalidTransport
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
	client, err := NewClient(ClientDependencies{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func callA2ARedirectOperation(t *testing.T, client *Client, endpoint, operation string) error {
	t.Helper()
	if operation == "discover" {
		_, err := client.Discover(t.Context(), endpoint)
		return err
	}
	_, err := client.SendMessage(t.Context(), hostedTestDiscovery(endpoint), SendRequest{
		MessageID: "redirect-message", Text: "redirect fixture",
	})
	return err
}
