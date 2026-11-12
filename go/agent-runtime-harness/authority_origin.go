package harness

import "strings"

// ValidReservedAuthorityOrigin is a pure write-admission guard shared by
// Memory and SQL. No Item may use either original physical-operation
// namespace unless it carries the complete first-party intent structure.
// These predicates run BEFORE any future child/settlement can refer to them.
func ValidReservedAuthorityOrigin(value Item) bool {
	switch value.ParentItemID {
	case CancellationFenceParentItemID:
		return ValidCancellationFenceIntentAdmission(value)
	case ExecutionDispatchParentItemID:
		_, valid := ValidExecutionDispatchGrantAdmission(value)
		return valid
	case DeferredChildGrantCancellationParentItemID:
		return ValidDeferredChildGrantCancellationProofAdmission(value)
	default:
		return !strings.HasPrefix(value.ID, "hcancelchildbusy_")
	}
}

// Reserved completion names have one original authorizing parent and may
// never be pre-occupied as arbitrary artifacts, orphan IDs or other Items.
func IsCancellationCompletionID(id string) bool {
	return strings.HasPrefix(id, "hcancelclosed_")
}

func IsExecutionDispatchCompletionID(id string) bool {
	return strings.HasPrefix(id, "hdispatchdone_")
}

// ValidReservedAuthorityCompletion binds a reserved completion ID to the
// actual original durable operation before either Store can replay/insert
// that ID. The exact payload, Run, Turn and Seq are separately enforced by
// the type-specific Verified*Receipt validators after sequence allocation.
func ValidReservedAuthorityCompletion(value Item, parent Item, found bool) bool {
	if IsCancellationCompletionID(value.ID) {
		return found && value.ParentItemID == parent.ID &&
			parent.Kind == ItemDiagnostic && parent.Status == ItemStarted &&
			parent.ParentItemID == CancellationFenceParentItemID
	}
	if IsExecutionDispatchCompletionID(value.ID) {
		return found && value.ParentItemID == parent.ID &&
			parent.Kind == ItemDiagnostic && parent.Status == ItemStarted &&
			parent.ParentItemID == ExecutionDispatchParentItemID
	}
	return true
}
