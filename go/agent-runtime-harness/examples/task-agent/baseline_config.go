package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
)

const (
	baselineSchemaVersion = 1
	baselineTaskVersion   = "release-recovery-v1"
	maxBaselineInputBytes = 64 << 10
)

type baselineInput struct {
	SchemaVersion               int    `json:"schemaVersion"`
	Provider                    string `json:"provider"`
	Endpoint                    string `json:"endpoint"`
	APIKey                      string `json:"apiKey"`
	Model                       string `json:"model"`
	ContextTokens               int64  `json:"contextTokens"`
	MaxOutputTokens             int64  `json:"maxOutputTokens"`
	Repetitions                 int    `json:"repetitions"`
	AllowUnknownStructuredModel bool   `json:"allowUnknownStructuredModel"`
}

// This separate type is the only configuration permitted in events.
type publicConfiguration struct {
	Provider                    string `json:"provider"`
	Model                       string `json:"model"`
	EndpointOrigin              string `json:"endpointOrigin"`
	EndpointHash                string `json:"endpointHash"`
	ContextTokens               int64  `json:"contextTokens"`
	MaxOutputTokens             int64  `json:"maxOutputTokens"`
	Repetitions                 int    `json:"repetitions"`
	AllowUnknownStructuredModel bool   `json:"allowUnknownStructuredModel"`
}

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)

func validModelID(value string) bool { return modelIDPattern.MatchString(value) }

func readBaselineInput(reader io.Reader) (baselineInput, publicConfiguration, error) {
	var input baselineInput
	raw, err := io.ReadAll(io.LimitReader(reader, maxBaselineInputBytes+1))
	if err != nil || len(raw) > maxBaselineInputBytes || !uniqueObjectFields(raw) {
		return input, publicConfiguration{}, errors.New("invalid configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return baselineInput{}, publicConfiguration{}, errors.New("invalid configuration")
	}
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || len(input.Endpoint) > 2048 {
		return baselineInput{}, publicConfiguration{}, errors.New("invalid configuration")
	}
	host := endpoint.Hostname()
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || !loopback) {
		return baselineInput{}, publicConfiguration{}, errors.New("invalid configuration")
	}
	if input.SchemaVersion != baselineSchemaVersion || (input.Provider != "openai" && input.Provider != "anthropic") ||
		!validModelID(input.Model) || input.APIKey == "" || strings.TrimSpace(input.APIKey) != input.APIKey ||
		len(input.APIKey) > 8192 || strings.ContainsAny(input.APIKey, "\r\n") ||
		input.Repetitions < 1 || input.Repetitions > 20 ||
		input.MaxOutputTokens < 1 || input.MaxOutputTokens > 1<<20 ||
		input.ContextTokens < input.MaxOutputTokens+serviceInputTokens || input.ContextTokens > 1<<24 ||
		strings.Contains(input.Model, input.APIKey) || strings.Contains(input.Endpoint, input.APIKey) {
		return baselineInput{}, publicConfiguration{}, errors.New("invalid configuration")
	}
	origin := (&url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host}).String()
	return input, publicConfiguration{
		Provider: input.Provider, Model: input.Model, EndpointOrigin: origin, EndpointHash: digest([]byte(input.Endpoint)),
		ContextTokens: input.ContextTokens, MaxOutputTokens: input.MaxOutputTokens, Repetitions: input.Repetitions,
		AllowUnknownStructuredModel: input.AllowUnknownStructuredModel,
	}, nil
}

func uniqueObjectFields(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		name, ok := token.(string)
		if tokenErr != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	last, err := decoder.Token()
	return err == nil && last == json.Delim('}')
}

func (input baselineInput) settings() settings {
	return settings{
		mode: "live", provider: input.Provider, modelID: input.Model, endpoint: input.Endpoint, apiKey: input.APIKey,
		contextTokens: input.ContextTokens, outputTokens: input.MaxOutputTokens,
		allowUnknownStructuredModel: input.AllowUnknownStructuredModel,
	}
}

func configurationError(output io.Writer) int {
	_ = json.NewEncoder(output).Encode(struct {
		Type        string `json:"type"`
		Stage       string `json:"stage"`
		FailureCode string `json:"failureCode"`
	}{Type: "error", Stage: "configuration", FailureCode: "invalid_configuration"})
	return 2
}

// Never parse Error() or copy Runtime.ErrorDetail: provider-controlled messages
// can contain credentials, even when a transport limits their length.
func failureCode(err error, fallback string) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, agent.ErrModelTimeout), errors.Is(err, agent.ErrToolTimeout):
		return "timeout"
	case errors.Is(err, model.ErrCapabilityUnavailable):
		return "capability_unavailable"
	case errors.Is(err, agent.ErrOutputSchema), errors.Is(err, model.ErrOutputContract):
		return "output_schema_invalid"
	case errors.Is(err, agent.ErrOutputRefused):
		return "output_refused"
	case errors.Is(err, openai.ErrIncomplete), errors.Is(err, anthropic.ErrIncomplete):
		return "provider_incomplete"
	case errors.Is(err, openai.ErrReplayUnavailable):
		return "provider_replay_unavailable"
	case errors.Is(err, openai.ErrUnsupported), errors.Is(err, anthropic.ErrUnsupported):
		return "provider_unsupported"
	case errors.Is(err, agent.ErrToolOutcomeUnknown):
		return "tool_outcome_unknown"
	case errors.Is(err, kernel.ErrExecutionConflict):
		return "execution_conflict"
	case errors.Is(err, runtimecontext.ErrBudgetExceeded):
		return "context_budget_exceeded"
	}
	if code := model.ClassifiedErrorCode(err); code != "" {
		return code
	}
	var failure taskFailure
	if errors.As(err, &failure) {
		return failure.code // Created only from fixed local constants.
	}
	return fallback
}
