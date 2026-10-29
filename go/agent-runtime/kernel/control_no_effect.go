package kernel

// controlPreStoreFailure is produced exclusively by Kernel.ApplyControl before
// entering Store.ApplyControl or Store.Apply. The attempted control has not
// reached a write-capable Store boundary; reading the authoritative Kernel
// snapshot, checking the expected revision and validating mutation/proof are
// read-only operations under the Kernel Store contract.
//
// Never attach this proof to any error returned by a write-capable Store
// method: even a conflict/error with no snapshot may follow a committed CAS.
type controlPreStoreFailure struct {
	cause error
}

func (e *controlPreStoreFailure) Error() string { return e.cause.Error() }
func (e *controlPreStoreFailure) Unwrap() error { return e.cause }

func controlNoEffectBeforeStore(cause error) error {
	if cause == nil {
		return nil
	}
	return &controlPreStoreFailure{cause: cause}
}

// controlStoreOutcomeError keeps all write-boundary failures untrusted, even
// if a custom Store forwards an error minted by a different Kernel operation.
// Its cause remains transparent to errors.Is/As but cannot be mistaken for
// this ApplyControl call's own direct pre-Store proof.
type controlStoreOutcomeError struct {
	cause error
}

func (e *controlStoreOutcomeError) Error() string { return e.cause.Error() }
func (e *controlStoreOutcomeError) Unwrap() error { return e.cause }

func controlStoreOutcome(cause error) error {
	if cause == nil {
		return nil
	}
	return &controlStoreOutcomeError{cause: cause}
}

// IsControlNoEffect reports only a direct Kernel-created pre-Store control
// failure. It intentionally does not traverse wrapped errors; a consumer must
// not mistake an externally wrapped error for Kernel write-boundary evidence.
// The original error remains inspectable with errors.Is/As.
func IsControlNoEffect(err error) bool {
	_, ok := err.(*controlPreStoreFailure) //nolint:errorlint // Proof requires a direct private result.
	return ok
}
