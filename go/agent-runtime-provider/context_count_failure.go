package provider

import (
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// InputCountFailureClass is a fixed, content-free classification of why an
// optional native counter did not establish an exact Count. It is not a
// Provider request/usage receipt and never authorizes Agent execution.
type InputCountFailureClass string

const (
	InputCountLocalRejected       InputCountFailureClass = "localRejected"
	InputCountHTTP4xx             InputCountFailureClass = "http4xx"
	InputCountHTTP5xx             InputCountFailureClass = "http5xx"
	InputCountOtherHTTP           InputCountFailureClass = "otherHTTP"
	InputCountTransportOrProtocol InputCountFailureClass = "transportOrProtocol"
	InputCountOther               InputCountFailureClass = "other"
)

// Classified failures retain errors.Is(..., ErrTokenCountUnavailable),
// with an Error() message independent of raw status/body/endpoint/credentials.
type countUnavailableError struct{ kind InputCountFailureClass }

func (err countUnavailableError) Error() string { return ErrTokenCountUnavailable.Error() }
func (err countUnavailableError) Unwrap() error { return ErrTokenCountUnavailable }

func tokenCountFailure(kind InputCountFailureClass) error {
	return countUnavailableError{kind: kind}
}

// ClassifyInputCountFailure observes only error kinds created by the Host
// Provider bridge. Unknown errors, including caller cancellation, do not
// acquire a fictitious upstream/transport origin.
func ClassifyInputCountFailure(err error) InputCountFailureClass {
	var classified countUnavailableError
	if errors.As(err, &classified) {
		return classified.kind
	}
	return InputCountOther
}

// classifyNativeInputFailure examines trusted local error types, not upstream
// message text. An HTTP class means a response status was actually observed;
// transportOrProtocol may include transport attempts *or* bad response JSON.
func classifyNativeInputFailure(err error) InputCountFailureClass {
	var status *providerhttp.StatusError
	if errors.As(err, &status) {
		switch {
		case status.Status >= 400 && status.Status <= 499:
			return InputCountHTTP4xx
		case status.Status >= 500 && status.Status <= 599:
			return InputCountHTTP5xx
		default:
			return InputCountOtherHTTP
		}
	}
	if errors.Is(err, openai.ErrUnsupported) ||
		errors.Is(err, openai.ErrReplayUnavailable) ||
		errors.Is(err, model.ErrCapabilityUnavailable) ||
		errors.Is(err, providerhttp.ErrConfig) {
		return InputCountLocalRejected
	}
	return InputCountTransportOrProtocol
}
