package kernel

// controlPreStoreFailure is produced exclusively by Kernel.ApplyControl before
// entering Store.ApplyControl or Store.Apply. The attempted control has not
// reached a write-capable Store boundary; reading the authoritative Kernel
// snapshot, checking the expected revision and validating mutation/proof are
// read-only operations under the Kernel Store contract.
//
// Never attach this proof to arbitrary errors returned by a write-capable
// Store. A separate, tightly typed ChildExecutionStore busy-no-effect proof
// is permitted only by its explicitly audited, atomic no-mutation contract;
// generic errors (including Store ErrConflict) can follow a committed CAS.
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

// controlChildBusyNoEffect is minted ONLY by Kernel for a ChildExecutionStore
// whose atomic contract guarantees ErrChildExecutionBusy was returned before
// mutating a parent Run. It is not a generic Store conflict or timeout proof.
// The Store has authoritative, immutable pending child Grants under its CAS.
type controlChildBusyNoEffect struct{ cause error }

func (e *controlChildBusyNoEffect) Error() string { return e.cause.Error() }
func (e *controlChildBusyNoEffect) Unwrap() error { return e.cause }

// IsControlNoEffect reports direct Kernel-created pre-Store failures OR the
// narrowly attested atomic ChildExecutionStore busy rejection. It intentionally does not traverse wrapped errors; a consumer must
// not mistake an externally wrapped error for Kernel write-boundary evidence.
// The original error remains inspectable with errors.Is/As.
func IsControlNoEffect(err error) bool {
	switch err.(type) { //nolint:errorlint // Direct private proof, no wrapped results.
	case *controlPreStoreFailure, *controlChildBusyNoEffect:
		return true
	default:
		return false
	}
}
