package main

import (
	"mime"
	"net/http"
	"strings"
	"sync"
)

type preflightResponseKind string

const (
	preflightJSON        preflightResponseKind = "json"
	preflightHTML        preflightResponseKind = "html"
	preflightEventStream preflightResponseKind = "event_stream"
	preflightOther       preflightResponseKind = "other"
)

// The observer retains only bounded transport facts, never the request,
// response, headers, body or transport error. It does not initiate requests.
type preflightHTTPObserver struct {
	transport    http.RoundTripper
	mu           sync.Mutex
	httpStatus   int
	responseKind preflightResponseKind
}

func newPreflightHTTPClient(base *http.Client) (*http.Client, *preflightHTTPObserver) {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	observer := &preflightHTTPObserver{transport: transport}
	client.Transport = observer
	// The adapter still applies providerhttp's timeout and redirect policy.
	// Using the original transport preserves its TLS and connection defaults.
	return client, observer
}

func (observer *preflightHTTPObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := observer.transport.RoundTrip(request)
	if response != nil && response.StatusCode >= 100 && response.StatusCode <= 999 {
		kind := classifyPreflightResponse(response.Header.Get("Content-Type"))
		observer.mu.Lock()
		observer.httpStatus, observer.responseKind = response.StatusCode, kind
		observer.mu.Unlock()
	}
	return response, err
}

func (observer *preflightHTTPObserver) snapshot() (int, preflightResponseKind) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.httpStatus, observer.responseKind
}

// Classify the declared media type only. Do not inspect or consume the body.
func classifyPreflightResponse(contentType string) preflightResponseKind {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return preflightOther
	}
	switch {
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return preflightJSON
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		return preflightHTML
	case mediaType == "text/event-stream":
		return preflightEventStream
	default:
		return preflightOther
	}
}
