package handoff

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoteCallbackSettlementContextIsBoundedAndDoesNotGrantPhysicalAuthority(t *testing.T) {
	type contextKey struct{}
	original, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "original"))
	cancel()
	settle, stop := RemoteCallbackSettlementContext(original)
	defer stop()
	if settle.Err() != nil || settle.Value(contextKey{}) != "original" {
		t.Fatal("original, cancelled caller cannot complete its physical callback evidence")
	}
	deadline, ok := settle.Deadline()
	until := time.Until(deadline)
	if !ok || until <= 0 || until > remoteCallbackSettlementTimeout {
		t.Fatalf("receipt settlement lacks a strict bounded deadline: %v %v", deadline, ok)
	}
	stop()
	if !errors.Is(settle.Err(), context.Canceled) {
		t.Fatal("bounded receipt settlement did not respect its own cancellation")
	}
}
