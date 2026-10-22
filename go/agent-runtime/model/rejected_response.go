package model

import "errors"

// ResponseRejectedError marks local rejection after a provider response has
// completed. The accompanying Response must contain authoritative cumulative
// Usage and all completed hosted Tool receipts. It never authorizes local Tools.
// Do not use this for transport failures, partial streams, or missing receipts.
type ResponseRejectedError interface {
	error
	ResponseRejected() bool
}

type responseRejectedError struct{ cause error }

func (e responseRejectedError) Error() string        { return e.cause.Error() }
func (e responseRejectedError) Unwrap() error        { return e.cause }
func (responseRejectedError) ResponseRejected() bool { return true }

// NewResponseRejectedError preserves the local cause and marks known completion.
func NewResponseRejectedError(cause error) error {
	if cause == nil {
		return nil
	}
	return responseRejectedError{cause: cause}
}

// IsResponseRejectedError recognizes the explicit completion contract through wrappers.
func IsResponseRejectedError(err error) bool {
	var rejected ResponseRejectedError
	return errors.As(err, &rejected) && rejected.ResponseRejected()
}
