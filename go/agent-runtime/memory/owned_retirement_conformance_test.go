package memory_test

import (
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestOwnedRetirementMemoryStoreConformance(t *testing.T) {
	conformance.RunOwnedRetirementStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		clock := &ownedPhysicalClock{now: time.Now().UTC()}
		s := memory.NewStoreWithExecutionClock(clock)
		return conformance.ManagedRecoveryStoreFixture{Store: s, Peer: s, AdvanceTo: func(target time.Time) {
			if clock.now.Before(target) {
				clock.now = target
			}
		}, Reopen: func() kernel.Store { return s }}
	})
}
