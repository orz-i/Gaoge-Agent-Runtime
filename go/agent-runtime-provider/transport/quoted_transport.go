package transport

import "net/http"

// The standard Transport may replay a POST after a reused-connection read
// failure when configured Idempotency-Key headers and GetBody are present.
// Such headers do not prove this upstream implements idempotency. Preserve
// the exact wire body and headers but make a dispatched body non-rewindable.
type quotedSingleAttemptTransport struct {
	next http.RoundTripper
}

func (transport quotedSingleAttemptTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	bound := request.Clone(request.Context())
	bound.GetBody = nil
	next := transport.next
	if next == nil {
		return nil, ErrHTTPClientRequired
	}
	return next.RoundTrip(bound)
}
