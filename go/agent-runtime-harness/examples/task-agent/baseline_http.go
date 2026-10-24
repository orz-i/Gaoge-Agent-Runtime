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

// The observer emits only bounded transport and optional wire structure facts.
// OpenAI probe bytes are captured transiently in memory; no raw body, headers
// or error text enters the event. It does not initiate requests.
type preflightHTTPObserver struct {
	transport    http.RoundTripper
	mu           sync.Mutex
	httpStatus   int
	responseKind preflightResponseKind
	captureWire  bool // Set before dispatch, only for the OpenAI probe.
	requestWire  probeWireRequestCheck
	responseWire *preflightWireCapture
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
	if observer.captureWire {
		check := observePreflightWireRequest(request)
		observer.mu.Lock()
		observer.requestWire, observer.responseWire = check, nil
		observer.mu.Unlock()
	}
	response, err := observer.transport.RoundTrip(request)
	if observer.captureWire && response != nil && response.Body != nil {
		capture := &preflightWireCapture{}
		response.Body = &preflightWireBody{body: response.Body, capture: capture}
		observer.mu.Lock()
		observer.responseWire = capture
		observer.mu.Unlock()
	}
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

func (observer *preflightHTTPObserver) wireSnapshot(normalized string) *probeWireCheck {
	observer.mu.Lock()
	request, response := observer.requestWire, observer.responseWire
	observer.mu.Unlock()
	if request.Capture == "" {
		request.Capture = wireUnavailable
	}
	return &probeWireCheck{Request: request, Response: observePreflightWireResponse(response, normalized)}
}

// Classify the declared media type independently of the optional wire capture.
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
