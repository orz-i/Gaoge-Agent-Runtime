package a2a

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const RunKind kernel.RunKind = "a2a.remote"

var ErrInvalidTransport = errors.New("invalid A2A transport")

// EndpointValidator applies host-owned outbound network policy before A2A requests.
type EndpointValidator interface {
	ValidateEndpoint(string) error
}

// EndpointValidatorFunc adapts a host validation function.
type EndpointValidatorFunc func(string) error

func (validator EndpointValidatorFunc) ValidateEndpoint(endpoint string) error {
	return validator(endpoint)
}

// HeaderProvider supplies request-scoped authentication or routing metadata.
type HeaderProvider interface {
	Headers(context.Context) (http.Header, error)
}

// HeaderProviderFunc adapts a request-scoped header function.
type HeaderProviderFunc func(context.Context) (http.Header, error)

func (provider HeaderProviderFunc) Headers(ctx context.Context) (http.Header, error) {
	return provider(ctx)
}

// TransportDependencies are explicit host-owned network dependencies.
type TransportDependencies struct {
	HTTPClient        *http.Client
	EndpointValidator EndpointValidator
	Headers           HeaderProvider
}

// Transport is the network seam used by the A2A wire adapter.
type Transport struct {
	client    *http.Client
	validator EndpointValidator
	headers   HeaderProvider
}

// NewTransport refuses implicit network defaults so security/tracing stay host-owned.
func NewTransport(dependencies TransportDependencies) (*Transport, error) {
	if dependencies.HTTPClient == nil || dependencies.EndpointValidator == nil {
		return nil, ErrInvalidTransport
	}
	return &Transport{
		client: dependencies.HTTPClient, validator: dependencies.EndpointValidator, headers: dependencies.Headers,
	}, nil
}

func (transport *Transport) prepare(ctx context.Context, rawEndpoint string) (string, http.Header, error) {
	if transport == nil || transport.client == nil || transport.validator == nil {
		return "", nil, ErrInvalidTransport
	}
	endpoint, err := normalizeEndpoint(rawEndpoint)
	if err != nil || transport.validator.ValidateEndpoint(endpoint) != nil {
		return "", nil, ErrInvalidTransport
	}
	headers := make(http.Header)
	if transport.headers != nil {
		provided, headerErr := transport.headers.Headers(ctx)
		if headerErr != nil {
			return "", nil, headerErr
		}
		headers = provided.Clone()
	}
	return endpoint, headers, nil
}

func normalizeEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", ErrInvalidTransport
	}
	return parsed.String(), nil
}

// RemoteAgentDescriptor is the immutable host-neutral discovery projection used
// by composition before a concrete A2A client is created.
type RemoteAgentDescriptor struct {
	Name            string
	Description     string
	AgentVersion    string
	PreferredURL    string
	ProtocolVersion string
	ProtocolBinding string
	Tenant          string
	Capabilities    []string
}

// CloneRemoteAgentDescriptor isolates discovered capability slices from cache mutation.
func CloneRemoteAgentDescriptor(descriptor RemoteAgentDescriptor) RemoteAgentDescriptor {
	descriptor.Capabilities = append([]string(nil), descriptor.Capabilities...)
	return descriptor
}
