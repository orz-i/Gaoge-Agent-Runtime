package agent

// nativeStartPreCreateNoEffect is minted by the concrete first-party Agent
// before Run Middleware or Kernel.Create. Its direct type is the authority,
// never ErrInvalidRequest, a generic CAS error, or an externally wrapped
// error. Catalog and capability lookup dependencies are read-only by contract.
type nativeStartPreCreateNoEffect struct {
	cause error
}

func (failure *nativeStartPreCreateNoEffect) Error() string { return failure.cause.Error() }
func (failure *nativeStartPreCreateNoEffect) Unwrap() error { return failure.cause }

func nativeStartNoEffect(cause error) error {
	if cause == nil {
		return nil
	}
	return &nativeStartPreCreateNoEffect{cause: cause}
}

// IsNativeStartNoEffect recognizes only this native Start call's direct
// pre-middleware/pre-Kernel failure. A public sentinel or wrapped error
// cannot certify absence of physical effects. The source cause remains
// inspectable with errors.Is and errors.As.
func IsNativeStartNoEffect(err error) bool {
	_, trusted := err.(*nativeStartPreCreateNoEffect) //nolint:errorlint // Exact provenance only.
	return trusted
}
