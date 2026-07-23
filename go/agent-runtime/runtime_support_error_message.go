package agentruntime

type messageError struct {
	cause   error
	message string
}

func (e messageError) Error() string { return e.message }
func (e messageError) Unwrap() error { return e.cause }

func withErrorMessage(cause error, message string) error {
	return messageError{cause: cause, message: message}
}
