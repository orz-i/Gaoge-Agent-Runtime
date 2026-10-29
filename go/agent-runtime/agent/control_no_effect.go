package agent

// nativeControlNoEffect is a first-party, pre-dispatch control result. The
// private concrete type, rather than an exported sentinel alone, is the
// authority: unrelated Run middleware or custom adapters cannot manufacture
// a native preflight receipt by returning ErrControlNoEffect.
type nativeControlNoEffect struct {
	cause error
}

func (e *nativeControlNoEffect) Error() string { return e.cause.Error() }
func (e *nativeControlNoEffect) Unwrap() error { return e.cause }
func (*nativeControlNoEffect) Is(target error) bool {
	return target == ErrControlNoEffect
}

func agentControlNoEffect(cause error) error {
	if cause == nil {
		return nil
	}
	return &nativeControlNoEffect{cause: cause}
}

// IsNativeControlNoEffect accepts only the direct first-party Agent control
// preflight return. It intentionally does not traverse wrapped errors: Run
// middleware and external adapters might return or wrap ErrControlNoEffect
// after executing effects. Underlying error causes remain available separately
// through errors.Is/As on the original error.
func IsNativeControlNoEffect(err error) bool {
	_, ok := err.(*nativeControlNoEffect) //nolint:errorlint // Provenance must reject wrapped errors.
	return ok
}
