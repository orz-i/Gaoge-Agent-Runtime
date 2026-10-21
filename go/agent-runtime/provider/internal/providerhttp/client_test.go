package providerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestRejectUnsafeEndpointsAndSecrets(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.com/v1", "http://127.0.0.2@localhost:8000/v1",
		"https://user:pass@api.example.com/v1", "https://example.com/v1?secret=x",
		"file:///tmp/socket", "/local/path",
	} {
		if _, err := New(Config{APIKey: "token", Endpoint: endpoint}, "https://api.example.com/v1", nil); !errors.Is(err, ErrConfig) {
			t.Errorf("unsafe endpoint accepted: %q, %v", endpoint, err)
		}
	}
	if _, err := New(Config{APIKey: ""}, "https://api.example.com", nil); !errors.Is(err, ErrConfig) {
		t.Fatal("missing secret accepted")
	}
}
func TestNoRedirectCredentialLeak(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("credential forwarded")
		}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := New(Config{APIKey: "my-token", Endpoint: source.URL}, source.URL,
		http.Header{"Authorization": {"Bearer my-token"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(context.Background(), map[string]string{"request": "hello"}, false)
	if resp != nil {
		_ = resp.Body.Close()
	}
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 307 || redirected.Load() != 0 {
		t.Fatalf("redirect not denied: err=%v count=%d", err, redirected.Load())
	}
}
func TestSSEFramesAndEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, ": comment")
		_, _ = fmt.Fprintln(w, "event: something")
		_, _ = fmt.Fprintln(w, "data: first")
		_, _ = fmt.Fprintln(w, "data: second")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "data: final")
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "my-token", Endpoint: server.URL}, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(context.Background(), map[string]string{"x": "y"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var calls []string
	if err := Stream(resp, func(event string, data []byte) error {
		calls = append(calls, event+":"+string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "something:first\nsecond" || calls[1] != ":final" {
		t.Fatalf("invalid SSE framing: %q", calls)
	}
}
func TestContextAndSanitizedFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "rate_limit", "message": "secret-token-should-not-return"},
		})
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "my-token", Endpoint: server.URL}, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(context.Background(), map[string]string{"x": "y"}, false)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !model.IsRetryableError(err) || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "my-token") {
		t.Fatalf("leak/retry bug: %v", err)
	}
	slow, err := New(Config{APIKey: "token", Endpoint: server.URL + "/slow",
		HTTPClient: &http.Client{Timeout: 25 * time.Millisecond}}, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	resp, err = slow.Post(ctx, map[string]string{"x": "y"}, false)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context deadline not respected: %v", err)
	}
}
