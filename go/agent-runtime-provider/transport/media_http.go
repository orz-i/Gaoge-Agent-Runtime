package transport

import (
	"bytes"
	"context"
	"net/http"
)

func (c *Client) doMediaJSONRequest(
	ctx context.Context,
	route RouteConfig,
	method string,
	requestURL string,
	payload []byte,
) ([]byte, *UpstreamDebugSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	if len(payload) > 0 {
		req.Header.Set("Content-Type", valueApplicationJson5943A7D2)
	}
	setBearerAuthHeader(req, route.APIKey)
	setAdditionalHeaders(req, route.HeadersJSON)
	return c.doMediaJSONHTTPRequest(route, req, payload)
}

func (c *Client) doMediaJSONHTTPRequest(route RouteConfig, req *http.Request, payload []byte) ([]byte, *UpstreamDebugSnapshot, error) {
	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	debug := upstreamDebugSnapshot(req, payload, resp, body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, debug, parseUpstreamError(resp.StatusCode, body, debug)
	}
	return body, debug, nil
}
