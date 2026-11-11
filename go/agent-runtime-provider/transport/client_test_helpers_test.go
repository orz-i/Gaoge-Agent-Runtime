package transport

import (
	"net/http"
	"time"
)

func newTestProviderClient() *Client {
	return NewClient(func(time.Duration) *http.Client {
		return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	})
}

const testResponsesEndpointPath = "/v1/responses"
