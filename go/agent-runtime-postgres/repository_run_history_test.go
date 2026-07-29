package postgres

import "testing"

func TestRunEventHistoryPredicateTreatsMissingCursorAsLatestPage(t *testing.T) {
	predicate, cursor := runEventHistoryPredicate(0)
	if predicate != "seq > ?" || cursor != 0 {
		t.Fatalf("latest predicate = %q cursor=%d", predicate, cursor)
	}

	predicate, cursor = runEventHistoryPredicate(-1)
	if predicate != "seq > ?" || cursor != 0 {
		t.Fatalf("negative cursor predicate = %q cursor=%d", predicate, cursor)
	}
}

func TestRunEventHistoryPredicateUsesExclusiveOlderCursor(t *testing.T) {
	predicate, cursor := runEventHistoryPredicate(42)
	if predicate != "seq < ?" || cursor != 42 {
		t.Fatalf("older predicate = %q cursor=%d", predicate, cursor)
	}
}
