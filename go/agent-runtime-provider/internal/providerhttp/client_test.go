package providerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type transportFailureFixture struct {
	timeout bool
	message string
	cause   error
}

func (failure *transportFailureFixture) Error() string { return failure.message }
func (failure *transportFailureFixture) Timeout() bool { return failure.timeout }
func (failure *transportFailureFixture) Unwrap() error { return failure.cause }
func (*transportFailureFixture) Temporary() bool       { return false }

type transportFailureRoundTripper func(*http.Request) (*http.Response, error)

func (transport transportFailureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestTransportTimeoutClassificationKeepsSafeCause(t *testing.T) {
	const private = "fake-private-transport-key"
	for _, tc := range []struct {
		name  string
		cause error
		code  string
	}{
		{"typed_timeout", &transportFailureFixture{timeout: true, message: private}, "model.upstream_timeout"},
		{"typed_not_timeout", &transportFailureFixture{timeout: false, message: "timeout " + private}, ""},
		{"text_only_timeout", errors.New("timeout " + private), ""},
		{"url_error_wrapped_timeout", &url.Error{
			Op: "Post", URL: "https://provider.invalid/" + private,
			Err: fmt.Errorf("wrapped %w", &transportFailureFixture{timeout: true, message: private}),
		}, "model.upstream_timeout"},
		{"false_wrapper_typed_timeout", &transportFailureFixture{
			message: private, cause: &transportFailureFixture{timeout: true, message: private},
		}, "model.upstream_timeout"},
		{"joined_later_timeout", errors.Join(
			&transportFailureFixture{message: "timeout " + private},
			fmt.Errorf("wrapped %w", &transportFailureFixture{timeout: true, message: private}),
		), "model.upstream_timeout"},
		{"joined_non_timeout", errors.Join(
			&transportFailureFixture{message: "timeout " + private}, errors.New("timeout "+private),
		), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Errorf("private transport URL %s: %w", private, tc.cause)
			var calls int
			client, err := New(Config{
				APIKey: private, Endpoint: "https://provider.invalid/" + private,
				HTTPClient: &http.Client{Transport: transportFailureRoundTripper(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, raw
				})},
			}, "https://provider.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Post(context.Background(), map[string]string{"x": "y"}, false)
			if response != nil {
				_ = response.Body.Close()
			}
			if response != nil || err == nil || calls != 1 || !model.IsRetryableError(err) ||
				!errors.Is(err, ErrProtocol) || model.ClassifiedErrorCode(err) != tc.code {
				t.Fatalf("transport failure changed retry or classification: response=%v error=%v calls=%d code=%q",
					response != nil, err, calls, model.ClassifiedErrorCode(err))
			}
			var rawURL *url.Error
			var rawNetwork *transportFailureFixture
			if errors.As(err, &rawURL) || errors.As(err, &rawNetwork) || errors.Is(err, raw) ||
				strings.Contains(err.Error(), private) || strings.Contains(err.Error(), "provider.invalid") {
				t.Fatal("returned error retained raw transport details")
			}
		})
	}
}

func TestCallerCancellationAndDeadlineTakePriorityOverTransportTimeout(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			defer cancel()
			client, err := New(Config{
				APIKey: "fake-caller-key", Endpoint: "https://provider.invalid/caller",
				HTTPClient: &http.Client{Transport: transportFailureRoundTripper(func(*http.Request) (*http.Response, error) {
					cancel()
					return nil, &transportFailureFixture{timeout: true, message: "fake-private-timeout"}
				})},
			}, "https://provider.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Post(ctx, map[string]string{"x": "y"}, false)
			if response != nil {
				_ = response.Body.Close()
			}
			if response != nil || !errors.Is(err, want) || model.IsRetryableError(err) ||
				model.ClassifiedErrorCode(err) != "" || errors.Is(err, ErrProtocol) {
				t.Fatalf("caller context no longer wins: err=%v", err)
			}
			if strings.Contains(err.Error(), "fake-private") {
				t.Fatal("caller cancellation exposed transport details")
			}
		})
	}
}

func TestHTTPClientTimeoutIsClassifiedWithoutExpiringCallerContext(t *testing.T) {
	var calls int
	client, err := New(Config{
		APIKey: "fake-client-timeout", Endpoint: "https://provider.invalid/private-timeout-path",
		HTTPClient: &http.Client{Timeout: 10 * time.Millisecond, Transport: transportFailureRoundTripper(func(request *http.Request) (*http.Response, error) {
			calls++
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
	}, "https://provider.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	response, err := client.Post(ctx, map[string]string{"x": "y"}, false)
	if response != nil {
		_ = response.Body.Close()
	}
	if response != nil || ctx.Err() != nil || calls != 1 || !model.IsRetryableError(err) ||
		model.ClassifiedErrorCode(err) != "model.upstream_timeout" || !errors.Is(err, ErrProtocol) ||
		errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client timeout lost classification or became caller expiry: error=%v calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), "provider.invalid") || strings.Contains(err.Error(), "private-timeout-path") {
		t.Fatal("client timeout exposed its request URL")
	}
}
