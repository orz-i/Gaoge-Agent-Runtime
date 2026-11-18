package transport

import (
	"errors"
	"net/http"
)

var ErrHTTPClientRequired = errors.New("provider transport requires a host HTTP client factory")

type deniedTransport struct{}

func (deniedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrHTTPClientRequired
}

type messageError struct {
	cause   error
	message string
}

func (e messageError) Error() string { return e.message }
func (e messageError) Unwrap() error { return e.cause }
func withMessage(cause error, message string) error {
	return messageError{cause: cause, message: message}
}
