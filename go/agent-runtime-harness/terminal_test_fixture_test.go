package harness

import "context"

// terminalFixtureContext belongs exclusively to in-package tests that need
// a pre-existing terminal historical state to probe retry, stale projection,
// or foreign ownership. It is never compiled into a production Harness
// artifact, never exported as a Host capability, and deliberately cannot
// stand in for an actual Kernel terminal result in production code.
//
// New security tests must NOT use this helper to prove unauthorized terminal
// transitions are blocked; those tests call the Store with ordinary context.
func terminalFixtureContext(ctx context.Context, owner Invocation, status TurnStatus) context.Context {
	return withTerminalWitness(ctx, owner, status, terminalFromKernel)
}

// TerminalFixtureContextForTest is exported ONLY from a *_test.go source
// so this module's external-package harness_test fixtures may model a
// previously verified native result without adding a production bypass.
// Other modules importing Harness cannot call this fixture constructor.
func TerminalFixtureContextForTest(ctx context.Context, owner Invocation, status TurnStatus) context.Context {
	return terminalFixtureContext(ctx, owner, status)
}

// TerminalFixtureInvocationForTest seeds an already persisted historical
// terminal Invocation in a MemoryStore. This is a test-only direct Store
// fixture, NOT a production status authority and never used to validate
// terminal admission itself.
func TerminalFixtureInvocationForTest(
	store *MemoryStore, value Invocation, revision uint64,
) (Invocation, error) {
	if store == nil || revision == 0 || !validInvocation(value) {
		return Invocation{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	before, found := store.invocations[value.ID]
	if !found || before.Revision != revision ||
		!sameInvocationIdentity(before, value) ||
		before.ExecutionRefID != value.ExecutionRefID {
		return Invocation{}, ErrConflict
	}
	value.Revision = revision + 1
	store.invocations[value.ID] = cloneInvocation(value)
	return cloneInvocation(value), nil
}

// TerminalFixtureTurnForTest directly seeds historical Turn state with
// original identity/revision checks. Available only to same-module tests.
func TerminalFixtureTurnForTest(
	store *MemoryStore, value Turn, revision uint64,
) (Turn, error) {
	if store == nil || revision == 0 || !validTurn(value) {
		return Turn{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	before, found := store.turns[value.ID]
	if !found || before.Revision != revision || !sameTurnIdentity(before, value) {
		return Turn{}, ErrConflict
	}
	value.Revision = revision + 1
	store.turns[value.ID] = value
	return value, nil
}
