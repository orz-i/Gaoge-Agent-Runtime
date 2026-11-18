package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuotedHTTPRedirectPolicyDoesNotMutateOrdinaryCachedClient(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		if request.URL.Path == "/start" {
			writer.Header().Set("Location", "/redirect-target")
			writer.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newTestProviderClient()
	ordinary := client.httpClientForRoute(RouteConfig{})
	for _, test := range []struct {
		name     string
		bound    bool
		requests int64
		status   int
	}{
		{"quoted-first", true, 1, http.StatusTemporaryRedirect},
		{"ordinary-after-quoted", false, 2, http.StatusOK},
		{"quoted-after-ordinary", true, 1, http.StatusTemporaryRedirect},
		{"ordinary-again", false, 2, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := attempts.Load()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/start", strings.NewReader("same generation body"))
			require.NoError(t, err)
			response, err := client.httpClientForRoute(RouteConfig{SingleProviderAttempt: test.bound}).Do(request)
			require.NoError(t, err)
			_, readErr := io.Copy(io.Discard, response.Body)
			require.NoError(t, response.Body.Close())
			require.NoError(t, readErr)
			require.Equal(t, test.status, response.StatusCode)
			require.Equal(t, test.requests, attempts.Load()-before)
			require.Same(t, ordinary, client.httpClientForRoute(RouteConfig{}))
			require.Nil(t, ordinary.CheckRedirect, "quoted policy must never leak into the shared ordinary client")
		})
	}
}
