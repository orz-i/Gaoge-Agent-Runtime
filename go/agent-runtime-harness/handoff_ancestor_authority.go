package harness

// validTerminalHandoffAncestorBindings is the shared immutable owner-chain
// check used both by normal Turn terminal projection and by cancellation of
// a replacement Root that has not reached physical Start. A valid terminal
// receipt is not authority if any frozen intermediate Agent is missing or
// has rotated to a different execution Attempt.
func validTerminalHandoffAncestorBindings(
	child Invocation, binding childRootBinding, owners map[string]Invocation,
) bool {
	for _, ancestor := range binding.Parents {
		parent, found := owners[ancestor.InvocationID]
		if !found || parent.ParentItemID == "" ||
			parent.ID == child.ID || parent.TurnID != child.TurnID ||
			invocationAttempt(parent) != ancestor {
			return false
		}
	}
	return true
}
