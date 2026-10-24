// Package providerhttp implements bounded JSON and SSE HTTP transport shared
// by provider adapters. Protocol-specific semantics stay in each adapter.
package providerhttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	maxRequestBytes  = 8 << 20
	maxResponseBytes = 16 << 20
	maxEventBytes    = 8 << 20
)

var ErrProtocol = errors.New("invalid provider protocol response")
var ErrConfig = errors.New("invalid provider HTTP configuration")

type Config struct {
	APIKey string
	// Endpoint is the full provider API URL. Defaults are provided by the
	// concrete adapter. HTTP is permitted only for explicit loopback tests.
	Endpoint   string
	HTTPClient *http.Client
}

type Client struct {
	url     string
	client  *http.Client
	headers http.Header
}

func New(config Config, defaultEndpoint string, headers http.Header) (*Client, error) {
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrConfig
	}
	host := parsed.Hostname()
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return nil, ErrConfig
	}
	if strings.TrimSpace(config.APIKey) == "" || strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, ErrConfig
	}
	client := &http.Client{}
	if config.HTTPClient != nil {
		*client = *config.HTTPClient
	}
	if client.Timeout == 0 {
		client.Timeout = 2 * time.Minute // Bound direct Generate calls without a deadline.
	}
	// Never forward provider credentials across redirects (including same-host
	// redirects). The host may supply its own transport for trusted proxies.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{url: endpoint, client: client, headers: headers.Clone()}, nil
}

func (c *Client) Post(ctx context.Context, payload any, stream bool) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequestBytes {
		return nil, ErrConfig
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, ErrConfig
	}
	for name, values := range c.headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	req.Header.Set("User-Agent", "gaoge-agent-runtime-provider/1")
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Model generation is logically retryable with the same invocation
		// identity, even when a network failure makes billing ambiguous.
		// Keep only a fixed timeout fact, never the raw URL, network address
		// or transport error. The caller's own cancellation still wins above.
		if transportTimedOut(err) {
			return nil, model.NewRetryableError(model.NewClassifiedError(ErrProtocol, "model.upstream_timeout"))
		}
		return nil, model.NewRetryableError(ErrProtocol)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		var result struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result)
		code := result.Error.Code
		if code == "" {
			code = result.Error.Type
		}
		// Never interpolate server-supplied messages, headers, keys or payloads.
		cause := &StatusError{Status: resp.StatusCode, Code: safeCode(code)}
		if resp.StatusCode == 408 || resp.StatusCode == 409 || resp.StatusCode == 425 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return nil, model.NewRetryableError(cause)
		}
		return nil, cause
	}
	return resp, nil
}

// transportTimedOut keeps looking after a non-timeout wrapper: http.Client's
// url.Error can report false when a typed timeout is behind another wrapper.
func transportTimedOut(err error) bool {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return transportTimedOut(cause)
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, cause := range joined.Unwrap() {
			if transportTimedOut(cause) {
				return true
			}
		}
	}
	return false
}

type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	if e.Code == "" {
		return "provider HTTP status " + strconv.Itoa(e.Status)
	}
	return fmt.Sprintf("provider HTTP status %d (%s)", e.Status, e.Code)
}

func safeCode(code string) string {
	if len(code) > 64 {
		return ""
	}
	for _, c := range code {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return ""
	}
	return code
}

// ReadJSON decodes a bounded success response; the caller owns Body.Close.
func ReadJSON(resp *http.Response, dest any) error {
	limited := &io.LimitedReader{R: resp.Body, N: maxResponseBytes + 1}
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > maxResponseBytes {
		return ErrProtocol
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return ErrProtocol
	}
	return nil
}

// Stream processes bounded SSE frames. The caller must see the provider's
// terminal event before treating a stream as successful.
// Stream does not close resp.Body; the caller owns Body.Close.
func Stream(resp *http.Response, onEvent func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxEventBytes)
	var event string
	var data bytes.Buffer
	dispatch := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		raw := bytes.TrimSuffix(data.Bytes(), []byte{10})
		err := onEvent(event, raw)
		event = ""
		data.Reset()
		return err
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), string(rune(13)))
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event = value
		case "data":
			if data.Len()+len(value)+1 > maxEventBytes {
				return ErrProtocol
			}
			data.WriteString(value)
			data.WriteByte(10)
		}
	}
	if scanner.Err() != nil {
		if errors.Is(scanner.Err(), context.Canceled) || errors.Is(scanner.Err(), context.DeadlineExceeded) {
			return scanner.Err()
		}
		return ErrProtocol
	}
	// SSE allows EOF after a final non-empty line. Terminal-event enforcement
	// is the protocol adapter's responsibility.
	return dispatch()
}

// ParseOptions permits only provider-neutral, explicitly supported options.
// Unknown fields fail closed instead of silently dropping caller preferences.
func ParseOptions(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, ErrConfig
	}
	options := make(map[string]any, len(items))
	for k, v := range items {
		if k != "temperature" && k != "top_p" {
			return nil, ErrConfig
		}
		var number float64
		if err := json.Unmarshal(v, &number); err != nil || number < 0 || number > 1 {
			return nil, ErrConfig
		}
		options[k] = number
	}
	return options, nil
}
