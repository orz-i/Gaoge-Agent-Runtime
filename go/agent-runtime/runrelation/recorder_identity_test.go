package runrelation_test

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Go permits different zero-sized allocations to share the same address.
// Pointer equality is therefore not a sound index-ownership receipt here.
type zeroSizedRecorder struct{}

func (*zeroSizedRecorder) Ensure(context.Context, runrelation.Draft) (runrelation.Relation, error) {
	return runrelation.Relation{}, nil
}

func TestSameRecorderDoesNotTrustZeroSizedPointerIdentity(t *testing.T) {
	first, second := &zeroSizedRecorder{}, &zeroSizedRecorder{}
	if runrelation.SameRecorder(first, second) || runrelation.SameRecorder(first, first) {
		t.Fatal("zero-sized recorder pointer is not sound identity proof")
	}
}
